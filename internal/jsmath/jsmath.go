// Package jsmath reproduces the floating-point results of the JavaScript math
// the TypeScript CLI runs on, so Go scores match it bit for bit.
//
// JavaScript rounds every arithmetic operation separately, while the Go
// compiler may fuse a*b+c into one FMA instruction on some architectures.
// Callers therefore wrap each product that feeds an addition in an explicit
// float64 conversion, which the Go specification guarantees is not fused.
package jsmath

import (
	"math"
	"runtime"
)

// contracted reports whether V8's C++ math on this architecture is built with
// floating-point contraction. Node's arm64 builds contract multiply-adds within
// an expression; x86-64 builds have no FMA in the baseline instruction set.
var contracted = runtime.GOARCH == "arm64"

const (
	ln2Hi = 6.93147180369123816490e-01
	ln2Lo = 1.90821492927058770002e-10
	lg1   = 6.666666666666735130e-01
	lg2   = 3.999999999940941908e-01
	lg3   = 2.857142874366239149e-01
	lg4   = 2.222219843214978396e-01
	lg5   = 1.818357216161805012e-01
	lg6   = 1.531383769920937332e-01
	lg7   = 1.479819860511658591e-01
)

func mul(a, b float64) float64 {
	return float64(a * b)
}

// madd is a*b+c as the C compiler that built V8 evaluates it.
func madd(a, b, c float64) float64 {
	if contracted {
		return math.FMA(a, b, c)
	}
	return mul(a, b) + c
}

// Log is Math.log: fdlibm's e_log.c, the implementation V8 ships.
func Log(x float64) float64 {
	bits := math.Float64bits(x)
	hx := int32(bits >> 32)
	if hx < 0x00100000 || hx >= 0x7ff00000 {
		// Zero, negative, subnormal, infinite and NaN inputs: every
		// implementation agrees on these, and ranking never produces them.
		return math.Log(x)
	}
	k := (hx >> 20) - 1023
	hx &= 0x000fffff
	i := (hx + 0x95f64) & 0x100000
	x = math.Float64frombits(uint64(uint32(hx|(i^0x3ff00000)))<<32 | bits&0xffffffff)
	k += i >> 20
	f := x - 1.0
	dk := float64(k)
	if (0x000fffff & (2 + hx)) < 3 {
		if f == 0 {
			if k == 0 {
				return 0
			}
			return madd(dk, ln2Hi, mul(dk, ln2Lo))
		}
		r := mul(mul(f, f), madd(-0.33333333333333333, f, 0.5))
		if k == 0 {
			return f - r
		}
		return madd(dk, ln2Hi, -(madd(-dk, ln2Lo, r) - f))
	}
	s := f / (2.0 + f)
	z := mul(s, s)
	i = hx - 0x6147a
	w := mul(z, z)
	j := 0x6b851 - hx
	t1 := mul(w, madd(w, madd(w, lg6, lg4), lg2))
	t2 := mul(z, madd(w, madd(w, madd(w, lg7, lg5), lg3), lg1))
	i |= j
	r := t2 + t1
	if i > 0 {
		hfsq := mul(mul(0.5, f), f)
		if k == 0 {
			return f - madd(-s, hfsq+r, hfsq)
		}
		return madd(dk, ln2Hi, -((hfsq - madd(s, hfsq+r, mul(dk, ln2Lo))) - f))
	}
	if k == 0 {
		return madd(-s, f-r, f)
	}
	return madd(dk, ln2Hi, -(madd(s, f-r, -mul(dk, ln2Lo)) - f))
}

// Round is Math.round: the nearest integer, halves toward +Infinity.
func Round(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	floor := math.Floor(x)
	if x-floor >= 0.5 {
		return floor + 1
	}
	return floor
}
