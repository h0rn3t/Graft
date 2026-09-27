package fixture

type Saver interface{ Save() }

func Use(s Saver, callback func()) {
	s.Save()
	callback()
}
