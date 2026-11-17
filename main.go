package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"httpaas/internal/config"
	"httpaas/internal/model"
	"httpaas/internal/provision"
	"httpaas/internal/store"
)

//go:embed web/templates/*.html web/static/*
var webFS embed.FS

type App struct {
	cfg   config.Config
	store *store.Store
	prov  *provision.Provisioner
	tpl   *template.Template
	opMu  sync.Mutex
}

type pageData struct {
	Domain        string
	Instances     []model.Instance
	Problems      []string
	HasPending    bool
	Flash         string
	FlashType     string
	ClearFlashURL bool
}

func main() {
	cfg, err := config.Load("config.json")
	if err != nil {
		log.Fatalf("Configuración: %v", err)
	}
	st, err := store.New(filepath.Join("data", "instances.json"))
	if err != nil {
		log.Fatalf("Datos: %v", err)
	}
	tpl := template.Must(template.New("index.html").Funcs(template.FuncMap{
		"fmtTime": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.Format("02/01/2006 15:04")
		},
	}).ParseFS(webFS, "web/templates/*.html"))
	app := &App{cfg: cfg, store: st, prov: provision.New(cfg), tpl: tpl}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", app.home)
	mux.HandleFunc("POST /instances", app.create)
	mux.HandleFunc("POST /instances/delete", app.delete)
	mux.HandleFunc("GET /api/instances", app.apiInstances)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	staticFS, _ := fs.Sub(webFS, "web/static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	srv := &http.Server{Addr: cfg.ListenAddr, Handler: logging(mux), ReadHeaderTimeout: 10 * time.Second}
	fmt.Printf("HTTPaaS iniciado en http://localhost%s\n", cfg.ListenAddr)
	fmt.Printf("Dominio administrado: %s\n", cfg.Domain)
	if p := app.prov.Ready(); len(p) > 0 {
		fmt.Println("ADVERTENCIA:")
		for _, x := range p {
			fmt.Println(" -", x)
		}
	}
	log.Fatal(srv.ListenAndServe())
}

func (a *App) home(w http.ResponseWriter, r *http.Request) {
	items := a.store.List()
	pending := false
	for _, it := range items {
		if it.Status == model.StatusCreating || it.Status == model.StatusDeleting {
			pending = true
		}
	}

	flash := ""
	flashType := "success"
	clearFlashURL := false
	op := strings.TrimSpace(r.URL.Query().Get("op"))
	host := strings.TrimSpace(r.URL.Query().Get("host"))

	if op != "" && host != "" {
		inst, exists := a.store.Get(host)
		switch op {
		case "create":
			if !exists {
				flash = "Preparando la creación de la instancia " + host + "..."
				flashType = "warning"
			} else {
				switch inst.Status {
				case model.StatusCreating:
					flash = "Instancia " + host + " en proceso de creación..."
					flashType = "warning"
				case model.StatusActive:
					flash = "La instancia " + host + " se creó y desplegó correctamente."
					flashType = "success"
					clearFlashURL = true
				case model.StatusError:
					flash = "La creación de la instancia " + host + " terminó con un error. Revisa el detalle en el dashboard."
					flashType = "danger"
					clearFlashURL = true
				}
			}
		case "delete":
			if !exists {
				flash = "La instancia " + host + " se eliminó correctamente, junto con su registro DNS."
				flashType = "success"
				clearFlashURL = true
			} else {
				switch inst.Status {
				case model.StatusDeleting:
					flash = "Instancia " + host + " en proceso de eliminación..."
					flashType = "warning"
				case model.StatusError:
					flash = "La eliminación de la instancia " + host + " terminó con un error. Revisa el detalle en el dashboard."
					flashType = "danger"
					clearFlashURL = true
				}
			}
		}
	} else if msg := strings.TrimSpace(r.URL.Query().Get("msg")); msg != "" {
		flash = msg
		clearFlashURL = true
	}

	data := pageData{
		Domain: a.cfg.Domain, Instances: items, Problems: a.prov.Ready(), HasPending: pending,
		Flash: flash, FlashType: flashType, ClearFlashURL: clearFlashURL,
	}
	if err := a.tpl.ExecuteTemplate(w, "index.html", data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func (a *App) create(w http.ResponseWriter, r *http.Request) {
	if problems := a.prov.Ready(); len(problems) > 0 {
		http.Error(w, "Entorno no preparado: "+strings.Join(problems, "; "), http.StatusServiceUnavailable)
		return
	}
	max := a.cfg.MaxUploadMB << 20
	r.Body = http.MaxBytesReader(w, r.Body, max+(1<<20))
	if err := r.ParseMultipartForm(max); err != nil {
		http.Error(w, "Formulario/ZIP demasiado grande o inválido: "+err.Error(), 400)
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.FormValue("hostname")))
	deploymentType := strings.TrimSpace(r.FormValue("deployment_type"))
	if deploymentType == "" {
		deploymentType = "static"
	}
	executable := strings.TrimSpace(r.FormValue("executable"))
	appArgs := strings.TrimSpace(r.FormValue("app_args"))
	appPort := 0
	if deploymentType == "executable" {
		var parseErr error
		appPort, parseErr = strconv.Atoi(strings.TrimSpace(r.FormValue("app_port")))
		if parseErr != nil {
			http.Error(w, "puerto interno inválido", 400)
			return
		}
		if err := provision.ValidateExecutableSettings(executable, appPort, appArgs); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	} else if deploymentType != "static" {
		http.Error(w, "tipo de despliegue no válido", 400)
		return
	}
	if err := provision.ValidateHostname(name); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if _, ok := a.store.Get(name); ok {
		http.Error(w, "ya existe una instancia gestionada con ese nombre", 409)
		return
	}
	if a.prov.VBox.Exists(name) {
		http.Error(w, "VirtualBox ya tiene una VM llamada "+name, 409)
		return
	}

	file, hdr, err := r.FormFile("sitezip")
	if err != nil {
		http.Error(w, "debes seleccionar un archivo .zip", 400)
		return
	}
	defer file.Close()
	if !strings.HasSuffix(strings.ToLower(hdr.Filename), ".zip") {
		http.Error(w, "el archivo debe ser .zip", 400)
		return
	}
	if err := os.MkdirAll("uploads", 0755); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	zipPath := filepath.Join("uploads", fmt.Sprintf("%d-%s.zip", time.Now().UnixNano(), name))
	out, err := os.Create(zipPath)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		os.Remove(zipPath)
		http.Error(w, err.Error(), 500)
		return
	}
	out.Close()
	if err := provision.ValidateZIP(zipPath, deploymentType, executable); err != nil {
		os.Remove(zipPath)
		http.Error(w, err.Error(), 400)
		return
	}

	used, err := a.prov.ReservedIPs()
	if err != nil {
		os.Remove(zipPath)
		http.Error(w, "no fue posible verificar las IPs ya ocupadas: "+err.Error(), 500)
		return
	}
	for _, it := range a.store.List() {
		used[it.IP] = true
	}
	ip, err := provision.AllocateIP(a.cfg.IPStart, a.cfg.IPEnd, used)
	if err != nil {
		os.Remove(zipPath)
		http.Error(w, err.Error(), 500)
		return
	}
	inst := model.Instance{Name: name, IP: ip, URL: a.cfg.URL(name), DeploymentType: deploymentType, Executable: executable, AppPort: appPort, AppArgs: appArgs, Status: model.StatusCreating, Message: "Solicitud recibida. Aprovisionando VM...", CreatedAt: time.Now()}
	if err := a.store.Upsert(inst); err != nil {
		os.Remove(zipPath)
		http.Error(w, err.Error(), 500)
		return
	}

	go func() {
		a.opMu.Lock()
		defer a.opMu.Unlock()
		defer os.Remove(zipPath)
		inst.Message = "Creando VM Multiattach, configurando red, DNS y despliegue " + deploymentType + "..."
		_ = a.store.Upsert(inst)
		if err := a.prov.Create(inst, zipPath); err != nil {
			inst.Status = model.StatusError
			inst.Message = err.Error()
			_ = a.store.Upsert(inst)
			return
		}
		inst.Status = model.StatusActive
		if deploymentType == "executable" {
			inst.Message = "Aplicación ejecutable desplegada correctamente mediante systemd + Apache reverse proxy"
		} else {
			inst.Message = "Sitio estático desplegado correctamente con Apache"
		}
		_ = a.store.Upsert(inst)
	}()
	http.Redirect(w, r, "/?op=create&host="+name, http.StatusSeeOther)
}

func (a *App) delete(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("hostname"))
	inst, ok := a.store.Get(name)
	if !ok {
		http.Error(w, "instancia no encontrada", 404)
		return
	}
	inst.Status = model.StatusDeleting
	inst.Message = "Eliminando registro DNS y máquina virtual..."
	_ = a.store.Upsert(inst)
	go func() {
		a.opMu.Lock()
		defer a.opMu.Unlock()
		if err := a.prov.Delete(inst); err != nil {
			inst.Status = model.StatusError
			inst.Message = err.Error()
			_ = a.store.Upsert(inst)
			return
		}
		_ = a.store.Delete(inst.Name)
	}()
	http.Redirect(w, r, "/?op=delete&host="+name, http.StatusSeeOther)
}

func (a *App) apiInstances(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(a.store.List())
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
