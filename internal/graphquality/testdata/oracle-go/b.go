package fixture

type B struct{}

func (B) Save() {}

func Left() { Right() }

func Right() { Left() }
