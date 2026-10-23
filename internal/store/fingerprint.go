package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// fingerprintColumns are the settings of the server: what an administrator
// changes, not what the server updates by itself (last seen, last fired,
// queues, statistics). A standby node copies the database of the active
// node when the fingerprint of these differs.
var fingerprintColumns = []struct{ table, columns, order string }{
	{"users", "id,username,email,name,password_hash,role,provider,external_id,disabled,language,language_manual,sites,role_managed,totp_secret,totp_enabled,recovery_codes", "id"},
	{"settings", "key, CASE key WHEN 'update' THEN json_remove(value, '$.last_check', '$.last_version') ELSE value END", "key"},
	{"site_ranges", "id,site,cidr", "id"},
	{"categories", "id,name,description,color,icon,query,pinned,sort", "id"},
	{"alert_rules", "id,name,query,threshold,window_minutes,cooldown_minutes,recipients,enabled,forward_to,channels,severity,email,condition,group_by,distinct_field,spike_percent,baseline_hours,schedule", "id"},
	{"api_tokens", "id,user_id,name,prefix,token_hash,read_only,expires_at", "id"},
	{"forward_destinations", "id,name,kind,enabled,filter,settings", "id"},
	{"notify_channels", "id,name,kind,enabled,config,settings", "id"},
	{"probes", "id,name,site,key_hash,code_hash,code_expires,settings,target_version", "id"},
	{"saved_searches", "id,owner_id,name,description,query,preset,range_from,range_to,site,shared,updated_at", "id"},
	{"saved_search_pins", "user_id,search_id", "user_id,search_id"},
	{"unifi_controllers", "id,name,url,api_key,site,verify_tls,enabled", "id"},
	{"devices", "key,name,notes,monitored", "key"},
	{"sources", "ip,name,site", "ip"},
}

// ConfigFingerprint hashes the settings of the server (see
// fingerprintColumns).
func (m *Meta) ConfigFingerprint() (string, error) {
	h := sha256.New()
	for _, t := range fingerprintColumns {
		n := strings.Count(t.columns, ",") + 1
		if strings.Contains(t.columns, "CASE") {
			n = 2
		}
		rows, err := m.DB.Query(fmt.Sprintf("SELECT %s FROM %s ORDER BY %s", t.columns, t.table, t.order))
		if err != nil {
			return "", fmt.Errorf("%s: %w", t.table, err)
		}
		vals := make([]any, n)
		ptrs := make([]any, n)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		fmt.Fprintf(h, "\x00%s\x00", t.table)
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return "", err
			}
			for _, v := range vals {
				fmt.Fprintf(h, "%v\x1f", v)
			}
			h.Write([]byte{0x1e})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:16]), nil
}
