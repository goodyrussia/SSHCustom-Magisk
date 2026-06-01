# WebUI ↔ API Contract Verification — REVIEW-pt5

## Summary: **12 mismatches found — CRITICAL: Profiles system completely broken**

---

## 1. GET /api/v1/status — StatusResponse fields

| WebUI reads | API struct tag | Match? |
|---|---|---|
| `st.connected` | `connected` | ✅ |
| `st.running` | **NOT PRESENT** | ❌ MISMATCH |
| `st.reconnecting` | **NOT PRESENT** | ❌ MISMATCH |
| `st.last_error` | `last_error` | ✅ |
| `st.profile` | `profile` | ✅ |
| `st.ssh_mode` | `ssh_mode` | ✅ |
| `st.ssh_host` | `ssh_host` | ✅ |
| `st.mem_mb` | `mem_mb` | ✅ |
| `st.memory_rss_bytes` | **NOT PRESENT** | ❌ MISMATCH |
| `st.cpu_pct` | `cpu_pct` | ✅ |
| `st.api_port` | **NOT PRESENT** | ❌ MISMATCH |
| `st.socks_port` | **NOT PRESENT** | ❌ MISMATCH |
| `st.tproxy_port` | **NOT PRESENT** | ❌ MISMATCH |
| `st.dns_port` | **NOT PRESENT** | ❌ MISMATCH |
| `st.routing_mode` | **NOT PRESENT** | ❌ MISMATCH |

**Line refs:** WebUI lines 721-789; contracts.go lines 5-16.

**Impact:** `st.running` and `st.reconnecting` drive the main connection state display — these will never be true, so the WebUI will always show "Disconnected" even when connected. Port/settings fields on the Settings tab (`api_port`, `socks_port`, `tproxy_port`, `dns_port`, `routing_mode`) will never populate.

---

## 2. GET /api/v1/profiles — Response field names (CRITICAL)

| WebUI reads | API struct tag | Match? |
|---|---|---|
| `data.profiles` | `items` | ❌ **MISMATCH** |
| `data.selected_id` | `current` | ❌ **MISMATCH** |

**Line refs:** WebUI lines 843-845; contracts.go lines 52-55.

**Impact:** After `loadProfiles()`, `profiles` = `data.profiles` = **`undefined`** (not `[]`). The fallback `|| []` saves it from crashing but the profiles list is always empty. `selectedProfileId` = `data.selected_id` = **`undefined`** (falls back to `''`).

The handler returns `{"current":"...","items":[...]}` but the WebUI expects `{"selected_id":"...","profiles":[...]}`. **This means profiles NEVER load in the WebUI.**

---

## 3. PUT /api/v1/profiles — Request body field names (CRITICAL)

| WebUI sends | API struct tag | Match? |
|---|---|---|
| `selected_id` | `current` | ❌ **MISMATCH** |
| `profiles` | `items` | ❌ **MISMATCH** |

**Line refs:** WebUI lines 880-881 (`selectProfile`), 958-962 (`saveProfile`), 976-981 (`deleteProfile`), 993-998 (`deleteProfileById`); contracts.go lines 52-55; handlers.go lines 230-231.

**Impact:** The handler decodes into `ProfilesResponse` which has `json:"current"` and `json:"items"`. The WebUI sends `selected_id` and `profiles`. The Go `json.Decoder` silently drops unknown fields, leaving `req.Current = ""` and `req.Items = nil`. **Profiles are NEVER saved, selected, or deleted.** Every PUT silently writes empty data.

---

## 4. ProfileItem missing `id` field

The WebUI relies on a per-profile `id` field for ALL profile operations:

| Usage | WebUI line | |
|---|---|---|
| Identity comparison | 859 | `p.id === selectedProfileId` |
| Display fallback | 865 | `p.name \|\| p.id` |
| Select button target | 870 | `selectProfile(p.id)` |
| Edit button target | 871 | `openEditor(p.id)` |
| Delete button target | 872 | `deleteProfileById(p.id)` |
| Open editor lookup | 892 | `profiles.find(x => x.id === id)` |
| Save — assign ID | 927 | `id: editingId \|\| undefined` |
| Save — generate new ID | 954 | `profile.id = 'pf_' + Date.now()` |
| Save — find existing | 950 | `updatedProfiles.findIndex(x => x.id === editingId)` |
| Delete — filter out | 976, 993 | `profiles.filter(x => x.id !== id)` |

**ProfileItem (contracts.go lines 31-49) has NO `id` field.** The handler constructs ProfileItem from config.Profile and never includes an ID. The WebUI generates ids client-side (`pf_` + timestamp), but since the API never returns/persists them, re-loaded profiles will have no id.

**Impact:** After a page reload, no profiles match `selectedProfileId`, the "Use" button disappears, editing targets nothing, delete buttons break.

---

## 5. GET /api/v1/latency — Response field name

| WebUI reads | API struct tag | Match? |
|---|---|---|
| `res.target` | `host` | ❌ **MISMATCH** |
| `res.latency_ms` | `latency_ms` | ✅ |

**Line ref:** WebUI line 831; contracts.go lines 70-75.

**Impact:** `res.target` is always `undefined`, so the latency display shows "TCP to undefined via tunnel" instead of showing the actual hostname.

---

## Summary Table

| # | Endpoint | Direction | WebUI name | API name | Severity |
|---|---|---|---|---|---|
| 1 | GET /status | JS reads | `running` | absent | HIGH |
| 2 | GET /status | JS reads | `reconnecting` | absent | HIGH |
| 3 | GET /status | JS reads | `memory_rss_bytes` | absent | LOW |
| 4 | GET /status | JS reads | `api_port` | absent | MEDIUM |
| 5 | GET /status | JS reads | `socks_port` | absent | MEDIUM |
| 6 | GET /status | JS reads | `tproxy_port` | absent | MEDIUM |
| 7 | GET /status | JS reads | `dns_port` | absent | MEDIUM |
| 8 | GET /status | JS reads | `routing_mode` | absent | MEDIUM |
| 9 | GET /profiles | JS reads | `profiles` | `items` | **CRITICAL** |
| 10 | GET /profiles | JS reads | `selected_id` | `current` | **CRITICAL** |
| 11 | PUT /profiles | JS sends | `selected_id` / `profiles` | `current` / `items` | **CRITICAL** |
| 12 | GET /latency | JS reads | `target` | `host` | LOW |
| — | ProfileItem | JS uses | `id` | absent | **CRITICAL** |

## Verdict

**The WebUI and API contracts are completely misaligned on the profiles system.** The field name disagreements (`profiles`/`items`, `selected_id`/`current`) mean the WebUI and API are effectively speaking different protocols for the entire profiles CRUD flow. Combined with the missing `id` field, profiles are impossible to create, read, update, or delete through the WebUI.

The status endpoint is also missing ~8 fields the WebUI reads, though the impact is less severe (displays fall back to defaults).

**The connection-status display (lines 721-757) uses `st.running` and `st.reconnecting` which don't exist in the API response — the "Connected" state will never activate even when the tunnel is up.**
