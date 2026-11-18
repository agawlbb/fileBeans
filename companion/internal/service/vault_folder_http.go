package service

import (
	"net/http"
	"path/filepath"
)

func vaultTrash(v VaultStatus) Trash {
	return Trash{ID: "vault-" + v.ID, Source: v.Source, Original: v.OriginalPath, Name: filepath.Base(v.OriginalPath), Deleted: v.Trashed, Expires: v.Trashed + 10*24*3600, State: "retained", Vault: v.ID}
}
func (c *brokerClient) folderAction(id, action, path string) error {
	if !validBrokerID(id) {
		return ErrUnsafe
	}
	return c.request("POST", "vaults/"+id+"/"+action, map[string]string{"path": path}, "", nil)
}
func (b *mountBroker) folderRoutes(mux *http.ServeMux) {
	for _, action := range []string{"relocate", "trash", "restore", "purge"} {
		mux.HandleFunc("POST /v1/vaults/{id}/"+action, func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				Path string `json:"path"`
			}
			if err := body(r, &input); err != nil {
				brokerFail(w, err)
				return
			}
			gate, err := b.exclusive()
			if err != nil {
				brokerFail(w, err)
				return
			}
			defer gate.Close()
			if err = b.folderAction(r.PathValue("id"), action, input.Path); err != nil {
				brokerFail(w, err)
				return
			}
			reply(w, 200, map[string]bool{"completed": true})
		})
	}
}
func (s *Service) vaultFolderRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/vaults/{id}/relocate", func(w http.ResponseWriter, r *http.Request) {
		if s.vaults == nil || s.vaults.remote == nil {
			fail(w, ErrUnsupported)
			return
		}
		var input struct {
			Path string `json:"path"`
		}
		if err := body(r, &input); err != nil {
			fail(w, err)
			return
		}
		if !s.vaultGate.TryLock() {
			fail(w, ErrVaultBusy)
			return
		}
		defer s.vaultGate.Unlock()
		if err := s.vaults.remote.folderAction(r.PathValue("id"), "relocate", input.Path); err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, map[string]bool{"completed": true})
	})
}
