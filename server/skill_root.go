package server

import (
	"net/http"
	"os"
)

func (s *Server) openSkillRoot(w http.ResponseWriter, name string) *os.Root {
	parent, err := os.OpenRoot(s.skillDir)
	if err != nil {
		writeErr(w, 404, "skill not found")
		return nil
	}
	defer parent.Close()
	root, err := parent.OpenRoot(name)
	if err != nil {
		writeErr(w, 404, "skill not found")
		return nil
	}
	return root
}
