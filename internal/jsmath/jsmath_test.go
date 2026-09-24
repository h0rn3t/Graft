package jsmath

import (
	"bufio"
	"math"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestLogIsArchitectureIndependent pins Log to Node's arm64 results on inputs
// where a separately rounded multiply-add would differ by one ulp.
func TestLogIsArchitectureIndependent(t *testing.T) {
	for _, tt := range []struct {
		n, d int
		want uint64
	}{
		{1, 108, 4576418140126336139},
		{8, 93, 4590549943063808273},
		{15, 135, 4592203442914244383},
		{15, 291, 4587380119386891579},
		{22, 48, 4600352432163822038},
	} {
		x := 1 + float64(tt.n)/float64(1+tt.d)
		if got := math.Float64bits(Log(x)); got != tt.want {
			t.Errorf("Log(1 + %d/%d) bits = %d, want %d", tt.n, 1+tt.d, got, tt.want)
		}
	}
}

// TestLogMatchesNode compares Log with Math.log bit for bit over the inputs
// ask's idf takes, when an arm64 node is available; Log follows Node's arm64
// results on every architecture.
func TestLogMatchesNode(t *testing.T) {
	if runtime.GOARCH != "arm64" {
		t.Skip("Log reproduces Node's arm64 builds")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	script := `const out = []; for (let n = 1; n <= 3000; n += 7) for (let d = 0; d <= 300; d += 3) {` +
		` const v = new Float64Array([Math.log(1 + n / (1 + d))]); out.push(new BigUint64Array(v.buffer)[0]); }` +
		` process.stdout.write(out.join("\n"));`
	output, err := exec.Command("node", "-e", script).Output()
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	mismatches := 0
	for n := 1; n <= 3000; n += 7 {
		for d := 0; d <= 300; d += 3 {
			if !scanner.Scan() {
				t.Fatal("node printed too few values")
			}
			want, _ := strconv.ParseUint(scanner.Text(), 10, 64)
			if got := Log(1 + float64(n)/float64(1+d)); math.Float64bits(got) != want {
				mismatches++
			}
		}
	}
	if mismatches > 0 {
		t.Errorf("Log differs from Math.log on %d inputs", mismatches)
	}
}

func TestRoundHalvesTowardPositiveInfinity(t *testing.T) {
	for input, want := range map[float64]float64{2.5: 3, -2.5: -2, 0.49999999999999994: 0, -0.5: -0, 1.4: 1} {
		if got := Round(input); got != want {
			t.Errorf("Round(%v) = %v, want %v", input, got, want)
		}
	}
}
