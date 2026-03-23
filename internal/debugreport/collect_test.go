package debugreport

import "testing"

func TestParseComposeServiceImagesIncludesMissingImageMarker(t *testing.T) {
	services, err := ParseComposeServiceImages([]byte(`services:
  app:
    image: nginx:1.27
  worker:
    build: .
`))
	if err != nil {
		t.Fatalf("ParseComposeServiceImages() error = %v", err)
	}
	if len(services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(services))
	}
	if services[0].Service != "app" || services[0].Image != "nginx:1.27" {
		t.Fatalf("unexpected first service: %#v", services[0])
	}
	if services[1].Service != "worker" || services[1].Image != "(no image field)" {
		t.Fatalf("unexpected second service: %#v", services[1])
	}
}

func TestParseComposeDiagnosticsExtractsBindMounts(t *testing.T) {
	services, bindMounts, err := ParseComposeDiagnostics([]byte(`services:
  app:
    image: nginx:1.27
    volumes:
      - type: bind
        source: /srv/data
        target: /data
      - app-data:/named
  worker:
    image: busybox
    volumes:
      - ./assets:/assets:ro
volumes:
  app-data: {}
`))
	if err != nil {
		t.Fatalf("ParseComposeDiagnostics() error = %v", err)
	}
	if len(services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(services))
	}
	if len(bindMounts) != 2 {
		t.Fatalf("expected 2 bind mounts, got %d (%#v)", len(bindMounts), bindMounts)
	}
	if bindMounts[0].Source != "./assets" || bindMounts[0].Target != "/assets" {
		t.Fatalf("unexpected first bind mount: %#v", bindMounts[0])
	}
	if bindMounts[1].Source != "/srv/data" || bindMounts[1].Target != "/data" {
		t.Fatalf("unexpected second bind mount: %#v", bindMounts[1])
	}
}

func TestParseMemInfoReturnsMemoryAndSwapBytes(t *testing.T) {
	memoryBytes, swapBytes, err := ParseMemInfo("MemTotal: 1024 kB\nSwapTotal: 2048 kB\n")
	if err != nil {
		t.Fatalf("ParseMemInfo() error = %v", err)
	}
	if memoryBytes != 1024*1024 {
		t.Fatalf("expected memory bytes %d, got %d", 1024*1024, memoryBytes)
	}
	if swapBytes != 2048*1024 {
		t.Fatalf("expected swap bytes %d, got %d", 2048*1024, swapBytes)
	}
}
