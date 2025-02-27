package main

import (
	_ "embed"

	"github.com/Vcity-Team/vcitychain/command/root"
	"github.com/Vcity-Team/vcitychain/licenses"
)

var (
	//go:embed LICENSE
	license string
)

func main() {
	licenses.SetLicense(license)

	root.NewRootCommand().Execute()
}
