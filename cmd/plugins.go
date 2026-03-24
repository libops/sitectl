package cmd

import "github.com/libops/sitectl/pkg/plugin"

// pluginHasConverge checks whether the named plugin has registered a converge runner.
func pluginHasConverge(pluginName string) bool {
	installed, ok := plugin.FindInstalled(pluginName)
	if !ok {
		return false
	}
	return installed.CanConverge
}

// pluginHasSet checks whether the named plugin has registered a set runner.
func pluginHasSet(pluginName string) bool {
	installed, ok := plugin.FindInstalled(pluginName)
	if !ok {
		return false
	}
	return installed.CanSet
}

// pluginHasValidate checks whether the named plugin has registered a validate runner.
func pluginHasValidate(pluginName string) bool {
	installed, ok := plugin.FindInstalled(pluginName)
	if !ok {
		return false
	}
	return installed.CanValidate
}
