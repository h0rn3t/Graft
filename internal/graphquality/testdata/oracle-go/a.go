package fixture

type A struct{}

func (A) Save() {}

func Direct(a A) { a.Save() }

func Recur(n int) {
	if n > 0 {
		Recur(n - 1)
	}
}
