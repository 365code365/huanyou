package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/keys"
)

// catalogResource describes one list inside config.Catalog that the admin
// panel can create / read / update / delete. To add a new admin-managed list:
// add the field to config.Catalog (+ validation in Normalize), write a
// resource definition like planResource below, and mount it in registerAdmin.
type catalogResource[T any] struct {
	path  string                       // URL segment, e.g. "plans"
	label string                       // used in error messages
	items func(c *config.Catalog) *[]T // the list being edited
	id    func(t *T) string            // primary key
	view  func(s *Server, t *T) any    // list/detail output (mask secrets here)
	// prepare runs before save. old is nil on create. It may fill defaults,
	// generate secrets, or carry over fields the client must not change.
	prepare func(s *Server, c *config.Catalog, t *T, old *T) error
	// beforeDelete may refuse the delete (e.g. still referenced).
	beforeDelete func(s *Server, r *http.Request, c *config.Catalog, t *T) error
	afterDelete  func(s *Server, id string)
	// order, if set, sorts the list response (the stored order is unchanged).
	order func(c *config.Catalog) []T
}

var errNotFound = errors.New("not found")

// mountCatalogResource registers GET (list), POST (create), GET/PUT/DELETE /{id}.
func mountCatalogResource[T any](s *Server, a func(string, http.HandlerFunc), res catalogResource[T]) {
	base := "/admin/api/" + res.path

	a("GET "+base, func(w http.ResponseWriter, _ *http.Request) {
		list := *res.items(s.cat())
		if res.order != nil {
			list = res.order(s.cat())
		}
		out := make([]any, 0, len(list))
		for i := range list {
			out = append(out, res.view(s, &list[i]))
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": len(out)})
	})

	a("GET "+base+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		list := *res.items(s.cat())
		for i := range list {
			if res.id(&list[i]) == r.PathValue("id") {
				writeJSON(w, http.StatusOK, res.view(s, &list[i]))
				return
			}
		}
		writeErr(w, http.StatusNotFound, "not_found", res.label+"不存在")
	})

	a("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		var item T
		if !decodeStrict(w, r, &item) {
			return
		}
		next, err := s.live.Update(func(c *config.Catalog) error {
			list := res.items(c)
			for i := range *list {
				if res.id(&(*list)[i]) == res.id(&item) {
					return config.Invalid(fmt.Sprintf("%s ID %q 已存在", res.label, res.id(&item)))
				}
			}
			if res.prepare != nil {
				if err := res.prepare(s, c, &item, nil); err != nil {
					return err
				}
			}
			*list = append(*list, item)
			return nil
		}, s.saveCatalog(r))
		if s.catalogErr(w, err) {
			return
		}
		writeStored(w, s, res, next, res.id(&item))
	})

	a("PUT "+base+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		var item T
		if !decodeStrict(w, r, &item) {
			return
		}
		id := r.PathValue("id")
		if res.id(&item) != id {
			writeErr(w, http.StatusBadRequest, "id_immutable", "ID 不可修改")
			return
		}
		next, err := s.live.Update(func(c *config.Catalog) error {
			list := res.items(c)
			for i := range *list {
				if res.id(&(*list)[i]) == id {
					old := (*list)[i]
					if res.prepare != nil {
						if err := res.prepare(s, c, &item, &old); err != nil {
							return err
						}
					}
					(*list)[i] = item
					return nil
				}
			}
			return errNotFound
		}, s.saveCatalog(r))
		if s.catalogErr(w, err) {
			return
		}
		writeStored(w, s, res, next, id)
	})

	a("DELETE "+base+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		_, err := s.live.Update(func(c *config.Catalog) error {
			list := res.items(c)
			for i := range *list {
				if res.id(&(*list)[i]) == id {
					if res.beforeDelete != nil {
						if err := res.beforeDelete(s, r, c, &(*list)[i]); err != nil {
							return err
						}
					}
					*list = append((*list)[:i], (*list)[i+1:]...)
					return nil
				}
			}
			return errNotFound
		}, s.saveCatalog(r))
		if s.catalogErr(w, err) {
			return
		}
		if res.afterDelete != nil {
			res.afterDelete(s, id)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
}

// writeStored responds with the item as stored (after Normalize filled defaults).
func writeStored[T any](w http.ResponseWriter, s *Server, res catalogResource[T], c *config.Catalog, id string) {
	list := *res.items(c)
	for i := range list {
		if res.id(&list[i]) == id {
			writeJSON(w, http.StatusOK, res.view(s, &list[i]))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) saveCatalog(r *http.Request) func(*config.Catalog) error {
	return func(c *config.Catalog) error { return s.store.SaveCatalog(r.Context(), c) }
}

// catalogErr writes the right status for a LiveCatalog.Update error. Returns true if err != nil.
func (s *Server) catalogErr(w http.ResponseWriter, err error) bool {
	var ve *config.ValidationError
	switch {
	case err == nil:
		return false
	case errors.Is(err, errNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "记录不存在")
	case errors.As(err, &ve):
		writeErr(w, http.StatusBadRequest, "invalid", ve.Error())
	default:
		s.internal(w, err)
	}
	return true
}

func decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求格式错误："+err.Error())
		return false
	}
	return true
}

// ---- resource definitions ----

func planResource() catalogResource[config.Plan] {
	return catalogResource[config.Plan]{
		path: "plans", label: "套餐",
		items: func(c *config.Catalog) *[]config.Plan { return &c.Plans },
		order: func(c *config.Catalog) []config.Plan { return c.PlansByPrice() },
		id:    func(p *config.Plan) string { return p.ID },
		view: func(s *Server, p *config.Plan) any {
			return struct {
				config.Plan
				Free bool `json:"free"`
			}{*p, p.ID == s.cat().FreePlan}
		},
		prepare: func(_ *Server, _ *config.Catalog, p *config.Plan, _ *config.Plan) error {
			p.ID = strings.TrimSpace(p.ID)
			p.Name = strings.TrimSpace(p.Name)
			return nil
		},
		beforeDelete: func(s *Server, r *http.Request, c *config.Catalog, p *config.Plan) error {
			if p.ID == c.FreePlan {
				return config.Invalid("这是免费套餐，请先在「系统设置」里更换或关闭免费套餐")
			}
			if c.Referral.Enabled && c.Referral.RewardPlanID == p.ID {
				return config.Invalid("该套餐是邀请奖励套餐，请先在「系统设置」里更换")
			}
			n, err := s.store.CountActiveByPlan(r.Context(), p.ID)
			if err != nil {
				return err
			}
			if n > 0 {
				return config.Invalid(fmt.Sprintf("还有 %d 个用户正在使用该套餐，建议改为「下架」而不是删除", n))
			}
			return nil
		},
	}
}

func nodeResource() catalogResource[config.Node] {
	return catalogResource[config.Node]{
		path: "nodes", label: "节点",
		items: func(c *config.Catalog) *[]config.Node { return &c.Nodes },
		id:    func(n *config.Node) string { return n.ID },
		view:  func(s *Server, n *config.Node) any { return s.adminNodeView(n) },
		prepare: func(_ *Server, _ *config.Catalog, n *config.Node, old *config.Node) error {
			n.ID, n.Name, n.Address = strings.TrimSpace(n.ID), strings.TrimSpace(n.Name), strings.TrimSpace(n.Address)
			if n.Reality.ServerName == "" {
				return config.Invalid("请填写 REALITY 伪装域名（SNI），例如 www.apple.com")
			}
			if n.RealityTarget == "" {
				n.RealityTarget = n.Reality.ServerName + ":443"
			}
			if old != nil {
				// Secrets are never edited through PUT: keep them from the stored node.
				n.Token = old.Token
				n.RealityPrivateKey = old.RealityPrivateKey
				if n.Reality.PublicKey == "" {
					n.Reality.PublicKey = old.Reality.PublicKey
				}
				if n.Reality.ShortID == "" {
					n.Reality.ShortID = old.Reality.ShortID
				}
				return nil
			}
			// Create: generate the agent token, REALITY keys and the XHTTP path.
			tok, err := keys.RandomToken(32)
			if err != nil {
				return err
			}
			n.Token = tok
			if n.Reality.PublicKey == "" {
				priv, pub, sid, err := keys.RealityKeys()
				if err != nil {
					return err
				}
				n.RealityPrivateKey, n.Reality.PublicKey, n.Reality.ShortID = priv, pub, sid
			} else if n.Reality.ShortID == "" {
				return config.Invalid("导入已有节点时需要同时填写公钥和 short id")
			}
			if n.XHTTPPath == "" {
				p, err := keys.ShortID()
				if err != nil {
					return err
				}
				n.XHTTPPath = "/" + p[:12]
			}
			return nil
		},
		afterDelete: func(s *Server, id string) { s.monitor.Forget(id) },
	}
}

func appResource() catalogResource[config.AppRec] {
	return catalogResource[config.AppRec]{
		path: "apps", label: "推荐应用",
		items: func(c *config.Catalog) *[]config.AppRec { return &c.Apps },
		id:    func(a *config.AppRec) string { return a.ID },
		view:  func(_ *Server, a *config.AppRec) any { return a },
		prepare: func(_ *Server, _ *config.Catalog, a *config.AppRec, _ *config.AppRec) error {
			a.ID, a.Name, a.URL = strings.TrimSpace(a.ID), strings.TrimSpace(a.Name), strings.TrimSpace(a.URL)
			return nil
		},
	}
}

func contactResource() catalogResource[config.Contact] {
	return catalogResource[config.Contact]{
		path: "contacts", label: "联系方式",
		items: func(c *config.Catalog) *[]config.Contact { return &c.Contacts },
		id:    func(k *config.Contact) string { return k.ID },
		view: func(_ *Server, k *config.Contact) any {
			return struct {
				config.Contact
				Link string `json:"link"`
			}{*k, k.Link()}
		},
		order: func(c *config.Catalog) []config.Contact {
			list := append([]config.Contact(nil), c.Contacts...)
			sort.SliceStable(list, func(i, j int) bool {
				if list[i].Group != list[j].Group {
					return list[i].Group == "service"
				}
				return list[i].Sort < list[j].Sort
			})
			return list
		},
	}
}
