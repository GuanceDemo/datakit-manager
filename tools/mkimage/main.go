// mkimage writes a container image as a `docker save`-style tar archive without needing
// Docker: one layer holding the static server binary, running as a non-root user.
//
// Load it with:  docker load -i <file>   |   nerdctl load -i <file>   |   ctr -n k8s.io images import <file>
package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"log"
	"os"
	"time"
)

const uid = 65532 // "nonroot"

func main() {
	binary := flag.String("binary", "", "path of the linux server binary")
	arch := flag.String("arch", "amd64", "image architecture (amd64 / arm64)")
	tag := flag.String("tag", "datakit-manager:latest", "image name:tag")
	out := flag.String("out", "", "output tar file")
	flag.Parse()
	if *binary == "" || *out == "" {
		log.Fatal("usage: mkimage -binary <file> -arch <amd64|arm64> -tag <name:tag> -out <file.tar>")
	}
	bin, err := os.ReadFile(*binary)
	if err != nil {
		log.Fatal(err)
	}
	// fixed timestamp: the same binary always yields the same image
	created := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// ---- layer
	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	dir := func(name string, mode int64, owner int) {
		must(tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name, Mode: mode, Uid: owner, Gid: owner, ModTime: created}))
	}
	file := func(name string, mode int64, data []byte) {
		must(tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: mode, Size: int64(len(data)), ModTime: created}))
		_, err := tw.Write(data)
		must(err)
	}
	dir("data/", 0o700, uid) // DKM_DATA_DIR, owned by the runtime user
	dir("tmp/", 0o1777, 0)
	dir("etc/", 0o755, 0)
	file("etc/passwd", 0o644, []byte("root:x:0:0:root:/:/sbin/nologin\nnonroot:x:65532:65532:nonroot:/data:/sbin/nologin\n"))
	file("etc/group", 0o644, []byte("root:x:0:\nnonroot:x:65532:\n"))
	file("datakit-manager-server", 0o755, bin)
	must(tw.Close())
	layerSum := sha256.Sum256(layer.Bytes())
	layerID := hex.EncodeToString(layerSum[:])

	// ---- image config
	config := map[string]any{
		"architecture": *arch,
		"os":           "linux",
		"created":      created.Format(time.RFC3339),
		"config": map[string]any{
			"User":         "65532:65532",
			"Env":          []string{"PATH=/", "DKM_LISTEN=:8080", "DKM_DATA_DIR=/data"},
			"Entrypoint":   []string{"/datakit-manager-server"},
			"WorkingDir":   "/",
			"ExposedPorts": map[string]any{"8080/tcp": map[string]any{}},
			"Volumes":      map[string]any{"/data": map[string]any{}},
			"Labels":       map[string]string{"org.opencontainers.image.title": "DataKit Manager"},
		},
		"rootfs":  map[string]any{"type": "layers", "diff_ids": []string{"sha256:" + layerID}},
		"history": []map[string]any{{"created": created.Format(time.RFC3339), "created_by": "datakit-manager build.sh web"}},
	}
	cfg, err := json.Marshal(config)
	must(err)
	cfgSum := sha256.Sum256(cfg)
	cfgName := hex.EncodeToString(cfgSum[:]) + ".json"

	manifest, err := json.Marshal([]map[string]any{{"Config": cfgName, "RepoTags": []string{*tag}, "Layers": []string{layerID + "/layer.tar"}}})
	must(err)

	// ---- docker-archive
	f, err := os.Create(*out)
	must(err)
	defer f.Close()
	aw := tar.NewWriter(f)
	put := func(name string, data []byte) {
		must(aw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: created}))
		_, err := aw.Write(data)
		must(err)
	}
	put(cfgName, cfg)
	must(aw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: layerID + "/", Mode: 0o755, ModTime: created}))
	put(layerID+"/layer.tar", layer.Bytes())
	put("manifest.json", manifest)
	must(aw.Close())
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
