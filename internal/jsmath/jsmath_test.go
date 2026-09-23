package jsmath

import (
	"bufio"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// TestLogMatchesNode compares Log with Math.log bit for bit over the inputs
// ask's idf takes, when node is available.
func TestLogMatchesNode(t *testing.T) {
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
		t.Errorf("Log differs from Math.log on %d inputs (GOARCH contraction = %v)", mismatches, contracted)
	}
}

func TestRoundHalvesTowardPositiveInfinity(t *testing.T) {
	for input, want := range map[float64]float64{2.5: 3, -2.5: -2, 0.49999999999999994: 0, -0.5: -0, 1.4: 1} {
		if got := Round(input); got != want {
			t.Errorf("Round(%v) = %v, want %v", input, got, want)
		}
	}
}
