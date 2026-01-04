package sneatnav

import "github.com/datatug/datatug-cli/pkg/sneatview/sneatv"

type LoginMenu struct {
	*sneatv.ButtonWithShortcut
}

func NewLoginMenu() *LoginMenu {
	loginMenu := &LoginMenu{
		ButtonWithShortcut: sneatv.NewButtonWithShortcut("Login", 'l'),
	}
	return loginMenu
}
