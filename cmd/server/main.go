// datakit-manager-server serves the same UI and backend as the desktop app over HTTP, for
// delivery as a container.
//
// Environment:
//
//	DKM_LISTEN     listen address            (default :8080)
//	DKM_DATA_DIR   clusters / kubeconfigs / backups (default /data)
//	DKM_USERNAME   basic-auth user           (default admin)
//	DKM_PASSWORD   basic-auth password       (default: random, printed at start-up)
//	DKM_TLS_CERT / DKM_TLS_KEY   serve HTTPS with this certificate (optional)
package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	// root CA bundle compiled in: the container image has no system certificates
	_ "golang.org/x/crypto/x509roots/fallback"

	"datakit-manager/frontend"
	"datakit-manager/internal/app"
)

var version = "dev"

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	listen := env("DKM_LISTEN", ":8080")
	dataDir := env("DKM_DATA_DIR", "/data")
	user := env("DKM_USERNAME", "admin")
	pass := os.Getenv("DKM_PASSWORD")
	if pass == "" {
		b := make([]byte, 9)
		_, _ = rand.Read(b)
		pass = hex.EncodeToString(b)
		log.Printf("DKM_PASSWORD 未设置，已生成随机密码：用户名 %s  密码 %s", user, pass)
	}

	svc, err := app.New(dataDir, version, true)
	if err != nil {
		log.Fatalf("初始化数据目录 %s 失败: %v", dataDir, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.Handle("/api/", auth(user, pass, apiHandler(svc)))
	mux.Handle("/", auth(user, pass, staticHandler(frontend.Dist())))

	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	cert, key := os.Getenv("DKM_TLS_CERT"), os.Getenv("DKM_TLS_KEY")
	log.Printf("DataKit Manager %s 已启动，监听 %s，数据目录 %s", version, listen, dataDir)
	if cert != "" && key != "" {
		err = srv.ListenAndServeTLS(cert, key)
	} else {
		err = srv.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func auth(user, pass string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 || subtle.ConstantTimeCompare([]byte(p), []byte(pass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="DataKit Manager", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// apiHandler: POST /api/<Method> with a JSON array of positional arguments.
// Response: {"result": ...} or {"error": "..."}.
func apiHandler(svc *app.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		reply := func(code int, v any) {
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(v)
		}
		if r.Method != http.MethodPost {
			reply(http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
			return
		}
		// a custom header keeps other sites from driving the API with the browser's credentials
		if r.Header.Get("X-Requested-With") != "datakit-manager" {
			reply(http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
		if err != nil {
			reply(http.StatusRequestEntityTooLarge, map[string]string{"error": "请求体过大"})
			return
		}
		var args []json.RawMessage
		if len(body) > 0 {
			if err := json.Unmarshal(body, &args); err != nil {
				reply(http.StatusBadRequest, map[string]string{"error": "参数必须是 JSON 数组"})
				return
			}
		}
		result, err := svc.Call(strings.TrimPrefix(r.URL.Path, "/api/"), args)
		if err != nil {
			reply(http.StatusOK, map[string]string{"error": err.Error()})
			return
		}
		reply(http.StatusOK, map[string]any{"result": result})
	})
}

func staticHandler(dist fs.FS) http.Handler {
	files := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}
