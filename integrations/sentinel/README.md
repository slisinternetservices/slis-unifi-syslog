# Microsoft Sentinel

UniFi Syslog Server sends events to a Log Analytics workspace (and so to Microsoft Sentinel) through the **Logs Ingestion API**: HTTPS to a data collection endpoint (DCE), through a data collection rule (DCR), into the custom table `UnifiSyslog_CL` and optionally `CommonSecurityLog`.

| File | What it is |
|---|---|
| `dcr-template.json` | ARM template: table `UnifiSyslog_CL`, DCE, DCR and the role assignment |
| `queries.kql` | Hunting queries |
| `workbook.json` | Workbook (overview, firewall, IPS, sign-ins, Wi-Fi, device health) |
| `analytics-rules.json` | Five scheduled analytics rules, deployed disabled |

## 1. Register an app in Entra ID

1. **Entra ID → App registrations → New registration**. Name it e.g. `unify-syslog-forwarder`; no redirect URI.
2. Note the **Directory (tenant) ID** and **Application (client) ID**.
3. **Certificates & secrets**: add a **client secret** (note the value; it expires, so plan renewal) or upload a **certificate** (public key, `.cer`/`.pem`; the app needs the private key).
4. **Enterprise applications** → open the same app → note its **Object ID**. This is the service principal object id the template needs; it is *not* the object id on the App registration page.

## 2. Deploy the DCR template

The workspace must be in the resource group you deploy to (the template creates the table on the workspace by name).

```sh
az deployment group create \
  --resource-group <workspace-rg> \
  --template-file dcr-template.json \
  --parameters workspaceResourceId=/subscriptions/<sub>/resourceGroups/<workspace-rg>/providers/Microsoft.OperationalInsights/workspaces/<workspace> \
               servicePrincipalObjectId=<enterprise-app-object-id>
```

Or in the portal: **Deploy a custom template → Build your own template in the editor**, paste the file, fill in the parameters.

Parameters: `workspaceResourceId` (required), `location` (default: resource group location; use the workspace region), `namePrefix` (default `unifi-syslog`), `servicePrincipalObjectId` (empty skips the role assignment; then grant **Monitoring Metrics Publisher** on the DCR yourself), `tableRetentionDays` (default 90).

Role assignments can take up to 30 minutes to take effect; until then the app gets HTTP 403.

## 3. Configure the destination in the app

**Forwarding → Add destination → Microsoft Sentinel**, then copy:

| App field | Where it comes from |
|---|---|
| Tenant ID, Client ID | Step 1 |
| Client secret or certificate | Step 1 |
| Logs ingestion endpoint | Output `logsIngestionEndpoint` |
| DCR immutable ID | Output `dcrImmutableId` (`dcr-…`) |
| Stream | Output `customStream` (`Custom-UnifiSyslog`) |
| CEF stream (split mode) | Output `cefStream` (`Custom-UnifiCommonSecurityLog`) |

Show the outputs again with `az deployment group show -g <workspace-rg> -n dcr-template --query properties.outputs`.

Use **Test** to send a test event; it appears in `UnifiSyslog_CL` (`RecordType == "test"`) after a few minutes. The first data in a new table can take 10-15 minutes.

## Table mode: custom or split

- **Custom** (default): every event goes to `UnifiSyslog_CL`. One table, all UniFi fields.
- **Split**: firewall and IPS events go to `CommonSecurityLog` (DeviceVendor `Ubiquiti`, DeviceProduct `UniFi`), everything else to `UnifiSyslog_CL`. Use this when existing Sentinel content (ASIM network session parsers, firewall workbooks, rules) expects `CommonSecurityLog`.

The queries, workbook and the IPS and port scan rules read both tables, so they work in either mode.

## 4. Import the workbook

**Sentinel → Workbooks → Add workbook → Edit → Advanced Editor** (`</>`), choose **Gallery Template**, replace the content with `workbook.json`, **Apply**, then **Save**.

## 5. Deploy the analytics rules

```sh
az deployment group create -g <workspace-rg> --template-file analytics-rules.json --parameters workspace=<workspace>
```

The rules are created **disabled**: SSH/admin brute force, high severity IPS alert, port scan from one source, VPN authentication failures and UniFi Access door forced open. Review the thresholds under **Sentinel → Analytics** and enable them.

## Cost tips

- Use the app's filters to forward only what you hunt on, e.g. firewall blocks, IPS, auth, VPN, admin and access, and leave out DNS, DHCP and most Wi-Fi events.
- The Forwarding page shows bytes per hour and an estimated daily volume; enter your price per GB there for an estimated monthly cost.
- Firewall `allow` logs are usually the largest share; log only blocks on the UniFi side or filter them out.
- `CommonSecurityLog` and `UnifiSyslog_CL` both use the Analytics plan; long-term retention beyond `tableRetentionDays` can be set as total retention (archive) on the table.
- Check volume with `Usage | where DataType in ("UnifiSyslog_CL", "CommonSecurityLog") | summarize GB = sum(Quantity) / 1024 by DataType, bin(TimeGenerated, 1d)`.
