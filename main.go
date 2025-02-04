/*
Copyright © 2023 Joe Corall <joe@libops.io>
*/
package main

import "github.com/libops/sitectl/cmd"

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cmd.SetVersionInfo(version, commit, date)
	cmd.Execute()
}
