package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	cacheDir      = ".sitectl/cache"
	cacheValidity = 12 * time.Hour
)

// CacheKey represents a structured cache key
type CacheKey struct {
	ResourceType string // "organization", "project", "site"
	Operation    string // "list", "get"
	ParentType   string // optional: parent resource type
	ParentID     string // optional: parent resource ID
	SubResource  string // optional: "firewall", "members", "secrets"
	ResourceID   string // optional: specific resource ID
}

// GetCachePath returns the file path for a cache key
func (k CacheKey) GetCachePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	parts := []string{
		homeDir,
		cacheDir,
		k.Operation,
	}

	if k.ParentType != "" && k.ParentID != "" {
		// Cached sub-resource: ~/.sitectl/cache/list/organization/<uuid>/firewall/list.resp
		parts = append(parts, k.ParentType, k.ParentID)
		if k.SubResource != "" {
			parts = append(parts, k.SubResource)
		}
	} else {
		// Cached resource: ~/.sitectl/cache/list/organization/list.resp
		parts = append(parts, k.ResourceType)
	}

	// Determine filename
	var filename string
	if k.ResourceID != "" {
		filename = fmt.Sprintf("%s.resp", k.ResourceID)
	} else {
		filename = "list.resp"
	}

	parts = append(parts, filename)
	return filepath.Join(parts...), nil
}

// Get retrieves a cached value if it exists and is not expired
func Get(key CacheKey, target interface{}) (bool, error) {
	path, err := key.GetCachePath()
	if err != nil {
		return false, err
	}

	// Check if file exists
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	// Check if cache is expired
	if time.Since(info.ModTime()) > cacheValidity {
		// Cache expired, delete it
		os.Remove(path)
		return false, nil
	}

	// Read cache file
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	// Unmarshal into target
	if err := json.Unmarshal(data, target); err != nil {
		// Cache corrupted, delete it
		os.Remove(path)
		return false, nil
	}

	return true, nil
}

// Set stores a value in the cache
func Set(key CacheKey, value interface{}) error {
	path, err := key.GetCachePath()
	if err != nil {
		return err
	}

	// Create directory structure
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	// Marshal value
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("failed to marshal cache data: %w", err)
	}

	// Write to file
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	return nil
}

// Invalidate removes a cached value
func Invalidate(key CacheKey) error {
	path, err := key.GetCachePath()
	if err != nil {
		return err
	}

	// Remove file if it exists
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

// InvalidatePattern removes all cache entries matching a pattern
// This is useful for invalidating all caches related to a resource
func InvalidatePattern(resourceType, resourceID string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	// Invalidate list cache for this resource type
	listKey := CacheKey{
		ResourceType: resourceType,
		Operation:    "list",
	}
	err = Invalidate(listKey)
	if err != nil {
		return fmt.Errorf("failed to invalidate cache: %w", err)
	}

	// If we have a specific resource ID, invalidate its get cache and all sub-resources
	if resourceID != "" {
		getKey := CacheKey{
			ResourceType: resourceType,
			Operation:    "get",
			ResourceID:   resourceID,
		}
		err = Invalidate(getKey)
		if err != nil {
			return fmt.Errorf("failed to invalidate cache: %w", err)
		}

		// Invalidate all sub-resource caches
		subResources := []string{"firewall", "members", "secrets"}
		for _, subResource := range subResources {
			subCacheDir := filepath.Join(homeDir, cacheDir, "list", resourceType, resourceID, subResource)
			if err := os.RemoveAll(subCacheDir); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}

	return nil
}

// Clear removes all cached data
func Clear() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	cachePath := filepath.Join(homeDir, cacheDir)
	if err := os.RemoveAll(cachePath); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

// HashID creates a short hash for cache keys (for very long IDs)
func HashID(id string) string {
	hash := sha256.Sum256([]byte(id))
	return hex.EncodeToString(hash[:8]) // Use first 8 bytes (16 hex chars)
}
