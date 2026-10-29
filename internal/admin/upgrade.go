package admin

import (
	"fmt"
	"net/http"

	"github.com/buffsystems/buff-connector/internal/config"
	"github.com/buffsystems/buff-connector/internal/plugin"
)

// Moving a connection to another version of its plugin.
//
// A connection is pinned to one version on purpose: a plugin must not change
// under a customer because we published something. So an upgrade is an ACT, and
// the act has one thing to check that installing never has to — the settings the
// operator already gave may not satisfy the new version's schema. Finding that
// out at startup, after the old version is gone, would leave a working
// integration broken by a button labelled "update".
//
// Downgrading is allowed. It is sometimes exactly what an operator needs — a
// version that broke them — and refusing it would leave them with nothing but
// editing YAML by hand.

// checkUpgrade reports whether a connection's existing settings still satisfy
// the version it is being moved to.
func checkUpgrade(inst config.PluginInstance, m *plugin.Manifest) error {
	if err := m.ValidateConfig(inst.Config); err != nil {
		return fmt.Errorf("настройки этого подключения не подходят версии %s: %w", m.Version, err)
	}
	return nil
}

// instanceUpgrade moves one connection to another installed version.
func (s *Server) instanceUpgrade(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID          string `json:"id"`
		Version     string `json:"version"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := readJSON(r, &req); err != nil || s.plugins == nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "неверный запрос"})
		return
	}
	cfg, err := config.LoadLenient(s.cfgPath)
	if err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	inst := findInstance(cfg, req.ID)
	if inst == nil {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "нет такого подключения"})
		return
	}
	installed, err := s.plugins.Store().Resolve(inst.Plugin, req.Version)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := checkUpgrade(*inst, installed.Manifest); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	from := inst.Version
	for i := range cfg.Plugins {
		if cfg.Plugins[i].ID == req.ID {
			cfg.Plugins[i].Version = req.Version
		}
	}
	if err := config.SaveIf(s.cfgPath, cfg, req.Fingerprint); err != nil {
		writeJSON(w, 409, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.applyPlugins()
	writeJSON(w, 200, map[string]any{"ok": true, "id": req.ID, "from": from, "to": req.Version})
}
