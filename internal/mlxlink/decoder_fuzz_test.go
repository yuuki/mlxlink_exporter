package mlxlink

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
)

// The fuzz targets below state the decoder's contract as properties: an
// untrustworthy envelope is the only thing that fails, a renamed key costs only
// the family it feeds, valid structured sections survive a round trip, and
// whatever is accepted is exported exactly as decoded, never padded with zeros.
//
// Run one with, for example (a short -fuzzminimizetime keeps the large JSON
// seeds from stalling a short run, see FUZZMINIMIZETIME in the Makefile):
//
//	go test ./internal/mlxlink -run '^$' -fuzz '^FuzzDecode$' -fuzztime 30s -fuzzminimizetime 1s

type fuzzFixture struct {
	name string
	raw  []byte
}

// fuzzFixtures returns every captured and derived response under
// testdata/mlxlink, in name order, as the seed corpus.
func fuzzFixtures(f *testing.F) []fuzzFixture {
	f.Helper()

	paths, err := filepath.Glob(filepath.Join("testdata", "mlxlink", "*.json"))
	if err != nil {
		f.Fatalf("list fixtures: %v", err)
	}
	if len(paths) == 0 {
		f.Fatal("no mlxlink fixtures under testdata/mlxlink")
	}
	fixtures := make([]fuzzFixture, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			f.Fatalf("read fixture: %v", err)
		}
		fixtures = append(fixtures, fuzzFixture{name: filepath.Base(path), raw: raw})
	}
	return fixtures
}

// FuzzDecode feeds mutations of the real captures to both decoders.
func FuzzDecode(f *testing.F) {
	for _, fixture := range fuzzFixtures(f) {
		f.Add(fixture.raw)
	}
	f.Add(minimalMlxlinkJSON)

	f.Fuzz(func(t *testing.T, raw []byte) {
		data, err := Decode(raw)
		pcie, pcieErr := DecodePCIeEye(raw)
		// Both judge the same envelope, so they must reach the same verdict.
		if (err == nil) != (pcieErr == nil) {
			t.Fatalf("Decode and DecodePCIeEye disagree on the envelope: %v / %v", err, pcieErr)
		}
		if err != nil {
			return
		}
		// Map iteration feeds the FEC and SerDes parsers; their output must not
		// depend on it, or the exposition would reorder between sweeps.
		if again, _ := Decode(raw); !reflect.DeepEqual(again, data) {
			t.Fatalf("Decode is not deterministic:\n%+v\n%+v", data, again)
		}

		checkPortData(t, data)
		checkEyeFamilies(t, "PCIe Eye", pcie.InitialFOM, pcie.LastFOM)
		checkNetworkExposition(t, data)
		checkPCIeEyeExposition(t, pcie)
	})
}

// FuzzDecodeEnvelope wraps a fuzzed result.output in an envelope and checks
// the verdict: a non-zero status always fails, and with a zero status the
// response fails exactly when result.output is not an object. Nothing inside
// the sections may fail a device.
func FuzzDecodeEnvelope(f *testing.F) {
	for _, fixture := range fuzzFixtures(f) {
		var doc struct {
			Result struct {
				Output json.RawMessage `json:"output"`
			} `json:"result"`
		}
		if err := json.Unmarshal(fixture.raw, &doc); err != nil {
			f.Fatalf("parse fixture %s: %v", fixture.name, err)
		}
		f.Add([]byte(doc.Result.Output), 0, "success")
	}
	f.Add([]byte(`{}`), 0, "success")
	f.Add([]byte(`{}`), 1, "Failed to open device")
	f.Add([]byte(`null`), 0, "")
	f.Add([]byte(`[]`), 0, "")
	f.Add([]byte(`"output"`), -1, "\xff")

	f.Fuzz(func(t *testing.T, output []byte, code int, message string) {
		// Only a well-formed value can be spliced into the envelope without
		// changing its structure.
		if !json.Valid(output) {
			return
		}
		quoted, err := json.Marshal(message)
		if err != nil {
			t.Fatalf("marshal message: %v", err)
		}
		raw := fmt.Appendf(nil, `{"result":{"output":%s},"status":{"code":%d,"message":%s}}`,
			output, code, quoted)
		if !json.Valid(raw) {
			// Nesting just below the JSON depth limit overflows it once wrapped.
			return
		}

		_, err = Decode(raw)
		var object map[string]json.RawMessage
		isObject := json.Unmarshal(output, &object) == nil && object != nil
		switch {
		case code != 0:
			if err == nil || !strings.Contains(err.Error(), "status "+strconv.Itoa(code)) {
				t.Fatalf("status %d: expected the status to fail decoding, got %v", code, err)
			}
		case isObject:
			if err != nil {
				t.Fatalf("a zero status with an object output must decode, got %v for %s", err, output)
			}
		default:
			if err == nil {
				t.Fatalf("a non-object result.output must fail decoding: %s", output)
			}
		}
	})
}

// sectionFamilies maps each top-level section the decoders read to the
// canonical name of the one family it feeds.
var sectionFamilies = map[string]string{
	fieldAliases[sectionOperational]:  sectionOperational,
	fieldAliases[sectionCounters]:     sectionCounters,
	fieldAliases[sectionModule]:       sectionModule,
	fieldAliases[sectionFECHistogram]: sectionFECHistogram,
	fieldAliases[sectionSerDesTX]:     sectionSerDesTX,
	fieldAliases[sectionEye]:          sectionEye,
	fieldAliases[sectionPCIeEye]:      sectionPCIeEye,
}

func decodedFamilies(data PortData, pcie PCIeEye) map[string]any {
	return map[string]any{
		sectionOperational:  data.Link,
		sectionCounters:     data.Counters,
		sectionModule:       data.Module,
		sectionFECHistogram: data.FECHistogram,
		sectionSerDesTX:     data.SerDesTX,
		sectionEye:          data.Eye,
		sectionPCIeEye:      pcie,
	}
}

// FuzzDecodeRenamedKey renames or removes one key of an accepted capture, as a
// new MFT release might. The response must still decode, and every family fed
// by another section must decode exactly as before.
func FuzzDecodeRenamedKey(f *testing.F) {
	type capture struct {
		name   string
		output map[string]json.RawMessage
		data   PortData
		pcie   PCIeEye
	}
	var captures []capture
	for _, fixture := range fuzzFixtures(f) {
		data, err := Decode(fixture.raw)
		if err != nil {
			// A rejected envelope has no families to keep apart.
			continue
		}
		pcie, err := DecodePCIeEye(fixture.raw)
		if err != nil {
			f.Fatalf("DecodePCIeEye(%s): %v", fixture.name, err)
		}
		output, err := decodeOutput(fixture.raw)
		if err != nil {
			f.Fatalf("decodeOutput(%s): %v", fixture.name, err)
		}
		captures = append(captures, capture{name: fixture.name, output: output, data: data, pcie: pcie})
	}
	for i := range captures {
		f.Add(uint8(i), uint8(0), uint8(0), false, "")
		f.Add(uint8(i), uint8(1), uint8(3), false, " ")
		f.Add(uint8(i), uint8(2), uint8(0), true, " (MFT 4.35)")
	}

	f.Fuzz(func(t *testing.T, captureIndex, sectionIndex, keyIndex uint8, wholeSection bool, suffix string) {
		// JSON coerces invalid UTF-8 in a key, which could collide with a key
		// the renaming did not touch.
		if !utf8.ValidString(suffix) {
			return
		}
		c := captures[int(captureIndex)%len(captures)]
		output := maps.Clone(c.output)
		names := slices.Sorted(maps.Keys(output))
		touched := names[int(sectionIndex)%len(names)]

		if wholeSection {
			if !renameKey(output, touched, suffix) {
				return
			}
		} else {
			var sec map[string]json.RawMessage
			if err := json.Unmarshal(output[touched], &sec); err != nil || len(sec) == 0 {
				return
			}
			keys := slices.Sorted(maps.Keys(sec))
			if !renameKey(sec, keys[int(keyIndex)%len(keys)], suffix) {
				return
			}
			rewritten, err := json.Marshal(sec)
			if err != nil {
				t.Fatalf("marshal section: %v", err)
			}
			output[touched] = rewritten
		}

		raw, err := json.Marshal(map[string]any{
			"result": map[string]any{"output": output},
			"status": map[string]any{"code": 0, "message": "success"},
		})
		if err != nil {
			t.Fatalf("marshal document: %v", err)
		}
		data, err := Decode(raw)
		if err != nil {
			t.Fatalf("%s: changing a key of %q failed the whole response: %v", c.name, touched, err)
		}
		pcie, err := DecodePCIeEye(raw)
		if err != nil {
			t.Fatalf("%s: changing a key of %q failed the PCIe response: %v", c.name, touched, err)
		}

		got, want := decodedFamilies(data, pcie), decodedFamilies(c.data, c.pcie)
		for family := range want {
			if family == sectionFamilies[touched] {
				continue
			}
			if !reflect.DeepEqual(got[family], want[family]) {
				t.Errorf("%s: changing a key of %q changed %s:\n got: %+v\nwant: %+v",
					c.name, touched, family, got[family], want[family])
			}
		}
	})
}

// renameKey moves m[key] to key+suffix, or removes it for an empty suffix. It
// refuses a new name that is taken or that names a section the decoders read:
// that would move data into another family rather than rename it away.
func renameKey(m map[string]json.RawMessage, key, suffix string) bool {
	value := m[key]
	delete(m, key)
	if suffix == "" {
		return true
	}
	renamed := key + suffix
	if _, taken := m[renamed]; taken {
		return false
	}
	if _, read := sectionFamilies[renamed]; read {
		return false
	}
	m[renamed] = value
	return true
}

// fuzzStream hands out fuzzer-chosen values, and zeroes once the input is
// used up, so that every input describes some document.
type fuzzStream struct{ data []byte }

func (s *fuzzStream) next() byte {
	if len(s.data) == 0 {
		return 0
	}
	b := s.data[0]
	s.data = s.data[1:]
	return b
}

func (s *fuzzStream) intn(n int) int { return int(s.next()) % n }

func (s *fuzzStream) uint64() uint64 {
	var v uint64
	for range 8 {
		v = v<<8 | uint64(s.next())
	}
	// Shifting spreads the values over every magnitude instead of mostly huge.
	return v >> (s.next() % 64)
}

func (s *fuzzStream) finite() float64 {
	v := math.Float64frombits(s.uint64() << (s.next() % 64))
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return float64(int8(s.next()))
	}
	return v
}

// pad surrounds a structural value with the whitespace the decoder trims.
func (s *fuzzStream) pad(value string) string {
	switch s.next() % 4 {
	case 1:
		return " " + value
	case 2:
		return value + "\t"
	case 3:
		return " " + value + " "
	default:
		return value
	}
}

// unreadableScore is a value no structured parser may read as a number.
func (s *fuzzStream) unreadableScore() string {
	choices := []string{"N/A", "", "inf", "-Inf", "NaN", "1e999", "0x", "12 [0..20]"}
	return choices[s.intn(len(choices))]
}

func formatFloat(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func valuesObject(values ...string) map[string][]string {
	return map[string][]string{"values": values}
}

// uniqueLanes draws count distinct lane numbers below limit, in fuzzer order.
func (s *fuzzStream) uniqueLanes(count, limit int) []int {
	seen := make(map[int]bool, count)
	lanes := make([]int, 0, count)
	for len(lanes) < count {
		lane := s.intn(limit)
		for seen[lane] {
			lane = (lane + 1) % limit
		}
		seen[lane] = true
		lanes = append(lanes, lane)
	}
	return lanes
}

func (s *fuzzStream) fecHistogram() (map[string]any, []FECHistogramBin) {
	section := map[string]any{"Header": valuesObject("Range", "Occurrences")}
	var want []FECHistogramBin
	for bin := range 1 + s.intn(24) {
		low := s.uint64()
		high := low
		if s.next()%2 == 1 {
			high = low + s.uint64()
			if high < low {
				high = math.MaxUint64
			}
		}
		rng := "[" + s.pad(strconv.FormatUint(low, 10)) + "]"
		if low != high || s.next()%2 == 1 {
			rng = "[" + s.pad(strconv.FormatUint(low, 10)) + ":" + s.pad(strconv.FormatUint(high, 10)) + "]"
		}
		occurrences := s.uint64()
		section["Bin "+strconv.Itoa(bin)] = valuesObject(s.pad(rng), s.pad(strconv.FormatUint(occurrences, 10)))
		want = append(want, FECHistogramBin{
			Bin: bin, ErrorCountMin: low, ErrorCountMax: high, Occurrences: occurrences,
		})
	}
	return section, want
}

// serDesTaps is the allowlist, restated here as the oracle's own table.
var serDesTaps = map[string]string{
	"fir_pre3": "pre3", "fir_pre2": "pre2", "fir_pre1": "pre1", "fir_main": "main", "fir_post1": "post1",
}

func (s *fuzzStream) serDesTX() (map[string]any, SerDesTX) {
	var header []string
	for _, parameter := range []string{"fir_pre3", "fir_pre2", "fir_pre1", "fir_main", "fir_post1", "drv_amp"} {
		if s.next()%4 != 0 {
			header = append(header, parameter)
		}
	}
	for i := range s.intn(3) {
		header = append(header, "vendor_param_"+strconv.Itoa(i))
	}
	if len(header) == 0 {
		header = []string{"fir_main"}
	}
	for i := len(header) - 1; i > 0; i-- {
		j := s.intn(i + 1)
		header[i], header[j] = header[j], header[i]
	}

	padded := make([]string, len(header))
	for i, parameter := range header {
		padded[i] = s.pad(parameter)
	}
	section := map[string]any{"Serdes TX parameters": valuesObject(padded...)}

	var want SerDesTX
	for _, lane := range s.uniqueLanes(1+s.intn(8), 64) {
		values := make([]string, len(header))
		var fir []SerDesFIRCoefficient
		var drive []LaneValue
		readable := true
		for i, parameter := range header {
			tap, isFIR := serDesTaps[parameter]
			if !isFIR && parameter != "drv_amp" {
				// Unknown parameters are skipped unparsed, whatever they hold.
				values[i] = s.unreadableScore()
				continue
			}
			if s.next()%16 == 0 {
				values[i] = s.unreadableScore()
				readable = false
				continue
			}
			value := s.finite()
			values[i] = s.pad(formatFloat(value))
			if isFIR {
				fir = append(fir, SerDesFIRCoefficient{Lane: lane, Tap: tap, Value: value})
			} else {
				drive = append(drive, LaneValue{Lane: lane, Value: value})
			}
		}
		section["Lane "+strconv.Itoa(lane)] = valuesObject(values...)
		if readable {
			want.FIRCoefficients = append(want.FIRCoefficients, fir...)
			want.DriveAmplitude = append(want.DriveAmplitude, drive...)
		}
	}
	sort.Slice(want.FIRCoefficients, func(i, j int) bool {
		a, b := want.FIRCoefficients[i], want.FIRCoefficients[j]
		if a.Lane != b.Lane {
			return a.Lane < b.Lane
		}
		return a.Tap < b.Tap
	})
	sort.Slice(want.DriveAmplitude, func(i, j int) bool {
		return want.DriveAmplitude[i].Lane < want.DriveAmplitude[j].Lane
	})
	return section, want
}

// eyeSections builds a network and a PCIe Eye section over the same lanes. A
// lane with one unreadable score is dropped from every family of the section
// that needs that score; PCIe needs only the two FOM fields.
func (s *fuzzStream) eyeSections() (network, pcie map[string]any, wantNetwork Eye, wantPCIe PCIeEye) {
	fields := []string{"Initial FOM", "Last FOM", "Upper Grades", "Mid Grades", "Lower Grades"}
	lanes := s.uniqueLanes(1+s.intn(16), 256)

	type row struct {
		lane       int
		values     []float64
		unreadable int // index of the unreadable field, or -1
	}
	rows := make([]row, 0, len(lanes))
	laneColumn := make([]string, 0, len(lanes))
	columns := make([][]string, len(fields))
	for _, lane := range lanes {
		r := row{lane: lane, values: make([]float64, len(fields)), unreadable: -1}
		if s.next()%8 == 0 {
			r.unreadable = s.intn(len(fields))
		}
		for i := range fields {
			r.values[i] = s.finite()
			if i == r.unreadable {
				columns[i] = append(columns[i], s.unreadableScore())
				continue
			}
			columns[i] = append(columns[i], s.pad(formatFloat(r.values[i])))
		}
		laneColumn = append(laneColumn, s.pad(strconv.Itoa(lane)))
		rows = append(rows, r)
	}

	network = map[string]any{"Lane": valuesObject(laneColumn...), "FOM Mode": valuesObject("ignored")}
	pcie = map[string]any{"Lane": valuesObject(laneColumn...)}
	for i, field := range fields {
		network[field] = valuesObject(columns[i]...)
		if i < 2 {
			pcie[field] = valuesObject(columns[i]...)
		}
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].lane < rows[j].lane })
	for _, r := range rows {
		if r.unreadable < 0 {
			wantNetwork.InitialFOM = append(wantNetwork.InitialFOM, LaneValue{Lane: r.lane, Value: r.values[0]})
			wantNetwork.LastFOM = append(wantNetwork.LastFOM, LaneValue{Lane: r.lane, Value: r.values[1]})
			wantNetwork.UpperGrade = append(wantNetwork.UpperGrade, LaneValue{Lane: r.lane, Value: r.values[2]})
			wantNetwork.MidGrade = append(wantNetwork.MidGrade, LaneValue{Lane: r.lane, Value: r.values[3]})
			wantNetwork.LowerGrade = append(wantNetwork.LowerGrade, LaneValue{Lane: r.lane, Value: r.values[4]})
		}
		if r.unreadable < 0 || r.unreadable >= 2 {
			wantPCIe.InitialFOM = append(wantPCIe.InitialFOM, LaneValue{Lane: r.lane, Value: r.values[0]})
			wantPCIe.LastFOM = append(wantPCIe.LastFOM, LaneValue{Lane: r.lane, Value: r.values[1]})
		}
	}
	return network, pcie, wantNetwork, wantPCIe
}

// FuzzOptionalSectionsRoundTrip encodes fuzzer-chosen, well-formed FEC, SerDes
// and Eye data in mlxlink's vendor shape and decodes it again: what the parsers
// accept must come back exactly, and only the lanes made unreadable on purpose
// may be dropped.
func FuzzOptionalSectionsRoundTrip(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("\x0f\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f"))
	f.Add([]byte(strings.Repeat("\xff\x00\x7f\x80\x10\x08", 64)))
	f.Add([]byte(strings.Repeat("mlxlink fuzz seed ", 32)))

	f.Fuzz(func(t *testing.T, seed []byte) {
		s := &fuzzStream{data: seed}
		fec, wantFEC := s.fecHistogram()
		serdes, wantSerDes := s.serDesTX()
		eye, pcieEye, wantEye, wantPCIe := s.eyeSections()

		raw, err := json.Marshal(map[string]any{
			"result": map[string]any{"output": map[string]any{
				fieldAliases[sectionFECHistogram]: fec,
				fieldAliases[sectionSerDesTX]:     serdes,
				fieldAliases[sectionEye]:          eye,
				fieldAliases[sectionPCIeEye]:      pcieEye,
			}},
			"status": map[string]any{"code": 0, "message": "success"},
		})
		if err != nil {
			t.Fatalf("marshal document: %v", err)
		}

		data, err := Decode(raw)
		if err != nil {
			t.Fatalf("Decode: %v\n%s", err, raw)
		}
		if !reflect.DeepEqual(data.FECHistogram, wantFEC) {
			t.Errorf("FEC histogram\n got: %+v\nwant: %+v\n%s", data.FECHistogram, wantFEC, raw)
		}
		if !reflect.DeepEqual(data.SerDesTX, wantSerDes) {
			t.Errorf("SerDes TX\n got: %+v\nwant: %+v\n%s", data.SerDesTX, wantSerDes, raw)
		}
		if !reflect.DeepEqual(data.Eye, wantEye) {
			t.Errorf("network Eye\n got: %+v\nwant: %+v\n%s", data.Eye, wantEye, raw)
		}
		pcie, err := DecodePCIeEye(raw)
		if err != nil {
			t.Fatalf("DecodePCIeEye: %v", err)
		}
		if !reflect.DeepEqual(pcie, wantPCIe) {
			t.Errorf("PCIe Eye\n got: %+v\nwant: %+v\n%s", pcie, wantPCIe, raw)
		}
	})
}

// unreadableReading is a scalar no measurement may be read from; unlike
// unreadableScore it excludes "12 [0..20]", which is how mlxlink reports a
// readable measurement.
func (s *fuzzStream) unreadableReading() string {
	choices := []string{"N/A", "n/a", "", "inf", "NaN", "1e999", "12abc", "1.2.3", "0x"}
	return choices[s.intn(len(choices))]
}

// measurements draws one reading per lane, an eighth of them unreadable, and
// returns them with the lanes a measurement family keeps: the readable ones,
// numbered by position and divided by the unit divisor.
func (s *fuzzStream) measurements(lanes int, divisor float64) ([]string, []LaneValue) {
	readings := make([]string, lanes)
	var want []LaneValue
	for lane := range lanes {
		if s.next()%8 == 0 {
			readings[lane] = s.unreadableReading()
			continue
		}
		value := s.finite()
		readings[lane] = s.pad(formatFloat(value))
		want = append(want, LaneValue{Lane: lane, Value: value / divisor})
	}
	return readings, want
}

// flags draws one 0/1 flag per lane, a sixteenth of them unreadable; one
// unreadable lane voids the whole family.
func (s *fuzzStream) flags(lanes int) ([]string, []LaneValue) {
	readings := make([]string, lanes)
	want := make([]LaneValue, 0, lanes)
	readable := true
	for lane := range lanes {
		if s.next()%16 == 0 {
			readings[lane] = []string{"2", "N/A", "", "01", "1.0", "true"}[s.intn(6)]
			readable = false
			continue
		}
		flag := float64(s.next() % 2)
		readings[lane] = s.pad(formatFloat(flag))
		want = append(want, LaneValue{Lane: lane, Value: flag})
	}
	if !readable {
		return readings, nil
	}
	return readings, want
}

// states draws one DataPath state per lane. Only the exact active state reads
// as 1; a lane without a state voids the family.
func (s *fuzzStream) states(lanes int) ([]string, []LaneValue) {
	readings := make([]string, lanes)
	want := make([]LaneValue, 0, lanes)
	readable := true
	for lane := range lanes {
		state := []string{"DPActivated", "DPActivated", "DPDeactivated", "DPInit", "dpactivated"}[s.intn(5)]
		if s.next()%16 == 0 {
			state = []string{"", "N/A", "n/a"}[s.intn(3)]
			readable = false
		}
		readings[lane] = s.pad(state)
		active := 0.0
		if state == datapathActivated {
			active = 1
		}
		want = append(want, LaneValue{Lane: lane, Value: active})
	}
	if !readable {
		return readings, nil
	}
	return readings, want
}

// FuzzLaneFamiliesRoundTrip encodes per lane readings, some unreadable on
// purpose, in the three shapes mlxlink uses and checks the two lane rules: a
// measurement family drops only its unreadable lanes and numbers the rest by
// position, while a flag family is published whole or not at all.
func FuzzLaneFamiliesRoundTrip(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("\x03\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f"))
	f.Add([]byte(strings.Repeat("\x00\x10\xff\x07", 128)))
	f.Add([]byte(strings.Repeat("lane families ", 32)))

	f.Fuzz(func(t *testing.T, seed []byte) {
		s := &fuzzStream{data: seed}
		lanes := 1 + s.intn(16)

		rawErrors, wantRawErrors := s.measurements(lanes, 1)
		rawBER, wantRawBER := s.measurements(lanes, 1)
		// Analog readings arrive comma-joined, usually with a range suffix.
		bias, wantBias := s.measurements(lanes, millisPerUnit)
		joinedBias := strings.Join(bias, ",")
		if s.next()%2 == 1 {
			joinedBias += " [40..480]"
		}
		txFault, wantTxFault := s.flags(lanes)
		rxLOS, wantRxLOS := s.flags(lanes)
		datapath, wantDatapath := s.states(lanes)

		raw, err := json.Marshal(map[string]any{
			"result": map[string]any{"output": map[string]any{
				fieldAliases[sectionCounters]: map[string]any{
					fieldAliases[fieldRawErrorsPerLane]: valuesObject(rawErrors...),
					fieldAliases[fieldRawBERPerLane]:    valuesObject(rawBER...),
				},
				fieldAliases[sectionModule]: map[string]any{
					fieldAliases[fieldBiasCurrent]:   joinedBias,
					fieldAliases[fieldTxFault]:       valuesObject(txFault...),
					fieldAliases[fieldRxLOS]:         valuesObject(rxLOS...),
					fieldAliases[fieldDatapathState]: valuesObject(datapath...),
				},
			}},
			"status": map[string]any{"code": 0, "message": "success"},
		})
		if err != nil {
			t.Fatalf("marshal document: %v", err)
		}
		data, err := Decode(raw)
		if err != nil {
			t.Fatalf("Decode: %v\n%s", err, raw)
		}

		for _, family := range []struct {
			name      string
			got, want []LaneValue
		}{
			{"raw physical errors", data.Counters.RawPhysicalErrorsLane, wantRawErrors},
			{"raw BER per lane", data.Counters.RawBERLane, wantRawBER},
			{"bias current", data.Module.BiasCurrentAmperes, wantBias},
			{"tx fault", data.Module.TxFault, wantTxFault},
			{"rx LOS", data.Module.RxLOS, wantRxLOS},
			{"datapath active", data.Module.DatapathActive, wantDatapath},
		} {
			if !reflect.DeepEqual(family.got, family.want) {
				t.Errorf("%s\n got: %+v\nwant: %+v\n%s", family.name, family.got, family.want, raw)
			}
		}
	})
}

func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// checkPortData asserts the invariants of any accepted response.
func checkPortData(t *testing.T, data PortData) {
	t.Helper()

	counters, module := data.Counters, data.Module
	for name, value := range map[string]Value{
		"effective physical errors": counters.EffectivePhysicalErrors,
		"link down":                 counters.LinkDown,
		"link error recovery":       counters.LinkErrorRecovery,
		"effective BER":             counters.EffectiveBER,
		"raw BER":                   counters.RawBER,
		"temperature":               module.TemperatureCelsius,
		"voltage":                   module.VoltageVolts,
	} {
		if value.Valid && !isFinite(value.Float) {
			t.Errorf("%s: valid but not finite: %v", name, value.Float)
		}
	}
	for name, value := range map[string]Value{
		"module FW fault":   module.ModuleFWFault,
		"datapath FW fault": module.DatapathFWFault,
	} {
		if value.Valid && value.Float != 0 && value.Float != 1 {
			t.Errorf("%s: flag is %v", name, value.Float)
		}
	}

	// Measurements drop single unreadable lanes, so only order is fixed.
	for name, lanes := range map[string][]LaneValue{
		"raw physical errors": counters.RawPhysicalErrorsLane,
		"raw BER per lane":    counters.RawBERLane,
		"bias current":        module.BiasCurrentAmperes,
		"rx power":            module.RxPowerDBm,
		"tx power":            module.TxPowerDBm,
	} {
		checkLanes(t, name, lanes)
	}
	// Flag families are all or nothing, so their lanes never have a gap.
	for name, lanes := range map[string][]LaneValue{
		"tx fault":        module.TxFault,
		"tx LOS":          module.TxLOS,
		"rx LOS":          module.RxLOS,
		"tx CDR LOL":      module.TxCDRLOL,
		"rx CDR LOL":      module.RxCDRLOL,
		"datapath active": module.DatapathActive,
	} {
		for i, lane := range lanes {
			if lane.Lane != i {
				t.Errorf("%s: lane %d at position %d, a partially decoded family", name, lane.Lane, i)
			}
			if lane.Value != 0 && lane.Value != 1 {
				t.Errorf("%s: lane %d flag is %v", name, lane.Lane, lane.Value)
			}
		}
	}

	for i, bin := range data.FECHistogram {
		if bin.Bin != i {
			t.Errorf("FEC histogram: bin %d at position %d, the bins must run 0..n-1", bin.Bin, i)
		}
		if bin.ErrorCountMin > bin.ErrorCountMax {
			t.Errorf("FEC histogram: bin %d range [%d:%d] is inverted", bin.Bin, bin.ErrorCountMin, bin.ErrorCountMax)
		}
	}

	checkSerDesTX(t, data.SerDesTX)
	eye := data.Eye
	checkEyeFamilies(t, "network Eye", eye.InitialFOM, eye.LastFOM, eye.UpperGrade, eye.MidGrade, eye.LowerGrade)
}

// checkLanes asserts strictly increasing, non-negative lanes with finite values,
// which also rules out a duplicate series.
func checkLanes(t *testing.T, name string, lanes []LaneValue) {
	t.Helper()

	for i, lane := range lanes {
		if lane.Lane < 0 {
			t.Errorf("%s: negative lane %d", name, lane.Lane)
		}
		if i > 0 && lane.Lane <= lanes[i-1].Lane {
			t.Errorf("%s: lane %d follows lane %d", name, lane.Lane, lanes[i-1].Lane)
		}
		if !isFinite(lane.Value) {
			t.Errorf("%s: lane %d is not finite: %v", name, lane.Lane, lane.Value)
		}
	}
}

func checkSerDesTX(t *testing.T, serdes SerDesTX) {
	t.Helper()

	known := make(map[string]bool, len(serDesTaps))
	for _, tap := range serDesTaps {
		known[tap] = true
	}
	tapsByLane := make(map[int][]string)
	for i, coefficient := range serdes.FIRCoefficients {
		if !known[coefficient.Tap] {
			t.Errorf("SerDes FIR: tap %q is not allowlisted", coefficient.Tap)
		}
		if !isFinite(coefficient.Value) {
			t.Errorf("SerDes FIR: lane %d tap %s is not finite", coefficient.Lane, coefficient.Tap)
		}
		if i > 0 {
			previous := serdes.FIRCoefficients[i-1]
			if previous.Lane > coefficient.Lane || (previous.Lane == coefficient.Lane && previous.Tap >= coefficient.Tap) {
				t.Errorf("SerDes FIR: %+v follows %+v", coefficient, previous)
			}
		}
		tapsByLane[coefficient.Lane] = append(tapsByLane[coefficient.Lane], coefficient.Tap)
	}
	checkLanes(t, "SerDes drive amplitude", serdes.DriveAmplitude)

	// Every lane is zipped with the same header and kept whole or not at all,
	// so the lanes that survive all carry the same parameters.
	drive := make(map[int]bool, len(serdes.DriveAmplitude))
	lanes := make(map[int]bool, len(tapsByLane))
	for lane := range tapsByLane {
		lanes[lane] = true
	}
	for _, lane := range serdes.DriveAmplitude {
		drive[lane.Lane] = true
		lanes[lane.Lane] = true
	}
	sorted := slices.Sorted(maps.Keys(lanes))
	for _, lane := range sorted[min(1, len(sorted)):] {
		first := sorted[0]
		if !slices.Equal(tapsByLane[lane], tapsByLane[first]) || drive[lane] != drive[first] {
			t.Errorf("SerDes: lane %d carries %v (drive %t), lane %d carries %v (drive %t)",
				lane, tapsByLane[lane], drive[lane], first, tapsByLane[first], drive[first])
		}
	}
}

// checkEyeFamilies asserts that every family of one Eye section covers the same
// lanes: a lane with one unreadable score is dropped from all of them.
func checkEyeFamilies(t *testing.T, name string, families ...[]LaneValue) {
	t.Helper()

	for i, family := range families {
		checkLanes(t, name, family)
		if len(family) != len(families[0]) {
			t.Errorf("%s: family %d has %d lanes, family 0 has %d", name, i, len(family), len(families[0]))
			continue
		}
		for j := range family {
			if family[j].Lane != families[0][j].Lane {
				t.Errorf("%s: family %d lane %d, family 0 lane %d", name, i, family[j].Lane, families[0][j].Lane)
			}
		}
	}
}

// gatherSeries collects through a pedantic registry, which fails on invalid
// label values, inconsistent descriptors and duplicate series, and counts the
// series of every family.
func gatherSeries(t *testing.T, collector prometheus.Collector) map[string]int {
	t.Helper()

	registry := prometheus.NewPedanticRegistry()
	if err := registry.Register(collector); err != nil {
		t.Fatalf("register collector: %v", err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	counts := make(map[string]int, len(families))
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, value := range []float64{metric.GetGauge().GetValue(), metric.GetCounter().GetValue()} {
				if !isFinite(value) {
					t.Errorf("%s exports a non-finite sample %v", family.GetName(), value)
				}
			}
		}
		counts[family.GetName()] = len(family.GetMetric())
	}
	return counts
}

func validCount(value Value) int {
	if value.Valid {
		return 1
	}
	return 0
}

func infoCount(values ...string) int {
	if anyNonEmpty(values) {
		return 1
	}
	return 0
}

// compareSeries asserts that the collector exported exactly want series per
// family and nothing else.
func compareSeries(t *testing.T, got, want map[string]int) {
	t.Helper()

	for name, count := range want {
		if got[name] != count {
			t.Errorf("%s: exported %d series, decoded %d", name, got[name], count)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("%s: exported, but not decoded", name)
		}
	}
}

// checkNetworkExposition exports fresh decoded data and checks that every
// decoded value becomes exactly one series and every dropped one none at all.
func checkNetworkExposition(t *testing.T, data PortData) {
	t.Helper()

	set := newDeviceSet([]DeviceSnapshot{{
		Target: collectorTarget, Data: data, LastSuccess: collectorSuccess, LastDuration: time.Second,
	}})
	got := gatherSeries(t, newTestCollector(t, set, collectorNow))

	link, counters, module, serdes, eye := data.Link, data.Counters, data.Module, data.SerDesTX, data.Eye
	info := module.Info
	compareSeries(t, got, map[string]int{
		"mlxlink_collector_up":                              1,
		"mlxlink_collection_duration_seconds":               1,
		"mlxlink_collection_last_success_timestamp_seconds": 1,

		"mlxlink_link_info": infoCount(link.State, link.PhysicalState, link.Speed, link.Width, link.FEC,
			link.AutoNegotiation),
		"mlxlink_module_info": infoCount(info.Identifier, info.Vendor, info.PartNumber, info.SerialNumber,
			info.Revision, info.FirmwareVersion, info.ActiveHostCompliance, info.ActiveMediaCompliance,
			info.CableType),

		"mlxlink_effective_physical_errors_total": validCount(counters.EffectivePhysicalErrors),
		"mlxlink_link_down_total":                 validCount(counters.LinkDown),
		"mlxlink_link_error_recovery_total":       validCount(counters.LinkErrorRecovery),
		"mlxlink_effective_physical_ber":          validCount(counters.EffectiveBER),
		"mlxlink_raw_physical_ber":                validCount(counters.RawBER),
		"mlxlink_module_temperature_celsius":      validCount(module.TemperatureCelsius),
		"mlxlink_module_voltage_volts":            validCount(module.VoltageVolts),
		"mlxlink_module_fw_fault":                 validCount(module.ModuleFWFault),
		"mlxlink_datapath_fw_fault":               validCount(module.DatapathFWFault),

		"mlxlink_raw_physical_errors_total":   len(counters.RawPhysicalErrorsLane),
		"mlxlink_raw_physical_ber_lane":       len(counters.RawBERLane),
		"mlxlink_module_bias_current_amperes": len(module.BiasCurrentAmperes),
		"mlxlink_module_rx_power_dbm":         len(module.RxPowerDBm),
		"mlxlink_module_tx_power_dbm":         len(module.TxPowerDBm),
		"mlxlink_tx_fault":                    len(module.TxFault),
		"mlxlink_tx_los":                      len(module.TxLOS),
		"mlxlink_rx_los":                      len(module.RxLOS),
		"mlxlink_tx_cdr_loss_of_lock":         len(module.TxCDRLOL),
		"mlxlink_rx_cdr_loss_of_lock":         len(module.RxCDRLOL),
		"mlxlink_datapath_active":             len(module.DatapathActive),

		"mlxlink_rx_fec_codewords_total":    len(data.FECHistogram),
		"mlxlink_serdes_tx_fir_coefficient": len(serdes.FIRCoefficients),
		"mlxlink_serdes_tx_drive_amplitude": len(serdes.DriveAmplitude),
		"mlxlink_eye_fom":                   len(eye.InitialFOM) + len(eye.LastFOM),
		"mlxlink_eye_grade":                 len(eye.UpperGrade) + len(eye.MidGrade) + len(eye.LowerGrade),
	})
}

func checkPCIeEyeExposition(t *testing.T, pcie PCIeEye) {
	t.Helper()

	set := newPCIeEyeSet([]PCIeEyeSnapshot{{
		Target: collectorTarget, Data: pcie, LastSuccess: collectorSuccess, LastDuration: time.Second,
	}})
	got := gatherSeries(t, newTestPCIeEyeCollector(set, collectorNow))
	compareSeries(t, got, map[string]int{
		"mlxlink_pcie_eye_collector_up":                              1,
		"mlxlink_pcie_eye_collection_duration_seconds":               1,
		"mlxlink_pcie_eye_collection_last_success_timestamp_seconds": 1,
		"mlxlink_pcie_eye_fom":                                       len(pcie.InitialFOM) + len(pcie.LastFOM),
	})
}

// BenchmarkDecode decodes each real network capture, the work a sweep does
// once per device.
func BenchmarkDecode(b *testing.B) {
	for _, name := range []string{
		"mft-4.34.1-400g-dr4.json",
		"mft-4.34.1-400g-fec-serdes.json",
		"mft-4.34.1-400g-eye.json",
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", "mlxlink", name))
		if err != nil {
			b.Fatalf("read fixture: %v", err)
		}
		b.Run(strings.TrimSuffix(name, ".json"), func(b *testing.B) {
			b.SetBytes(int64(len(raw)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := Decode(raw); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkDecodePCIeEye decodes the real sixteen-lane root PCIe Eye capture.
func BenchmarkDecodePCIeEye(b *testing.B) {
	raw, err := os.ReadFile(filepath.Join("testdata", "mlxlink", "mft-4.34.1-pcie-eye.json"))
	if err != nil {
		b.Fatalf("read fixture: %v", err)
	}
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := DecodePCIeEye(raw); err != nil {
			b.Fatal(err)
		}
	}
}
