package cmd

import (
	"fmt"
	"strings"

	"github.com/libops/sitectl/pkg/plugin"
)

func pluginSupportsConverge(pluginName string) (bool, error) {
	installed, err := installedPluginWithMetadata(pluginName)
	if err != nil {
		return false, err
	}
	return installed.CanConverge, nil
}

func pluginSupportsSet(pluginName string) (bool, error) {
	installed, err := installedPluginWithMetadata(pluginName)
	if err != nil {
		return false, err
	}
	return installed.CanSet, nil
}

func pluginSupportsDebug(pluginName string) (bool, error) {
	installed, err := installedPluginWithMetadata(pluginName)
	if err != nil {
		return false, err
	}
	return installed.CanDebug, nil
}

func pluginSupportsValidate(pluginName string) (bool, error) {
	installed, err := installedPluginWithMetadata(pluginName)
	if err != nil {
		return false, err
	}
	return installed.CanValidate, nil
}

func installedPluginWithMetadata(pluginName string) (plugin.InstalledPlugin, error) {
	installed, ok := plugin.FindInstalled(pluginName)
	if !ok {
		return plugin.InstalledPlugin{}, fmt.Errorf("plugin %q is not installed", pluginName)
	}
	if strings.TrimSpace(installed.MetadataError) != "" {
		return plugin.InstalledPlugin{}, fmt.Errorf("inspect plugin %q metadata: %s", pluginName, installed.MetadataError)
	}
	return installed, nil
}
