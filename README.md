# PK95 Monitoring

Monitoring für Server, Netzwerk und Dienste im Design von PK95 Core – ein Docker-Container je Kunde, ein
Go-Programm mit SQLite und eigener Weboberfläche (dieselbe Optik wie PK95 ProxBackup). Überwacht werden:

- **Proxmox VE über die API:** Cluster (Quorum, Knoten online), Knoten (CPU, RAM, Last, Root-Platte), VMs und
  Container (laufen sie, die mit „Beim Booten starten“ laufen müssen?), Speicher, ZFS-Pools, Platten (SMART,
  SSD-Verschleiß), Updates und Sicherungen (fehlgeschlagene vzdump-Aufgaben der letzten 24 Stunden). Ein Token mit
  Leserechten auf einem Knoten genügt, auch für einen ganzen Cluster.
- **Linux- und Windows-Server mit dem PK95-Agent:** CPU, Arbeitsspeicher, Auslagerung, Festplatten, Netzwerk,
  Dienste (systemd-Units bzw. Windows-Dienste, auf Wunsch alle automatisch startenden), fehlgeschlagene
  systemd-Units, Updates (apt, dnf, Windows Update; Neustart nötig = Warnung; unter Windows auch letzte
  Installation und fehlgeschlagene Updates) und unter Windows das Ereignisprotokoll – nach Regeln (Festplattenfehler,
  Bluescreen, fehlgeschlagene Sicherung, abgestürzte Dienste, Anmeldeversuche …) statt jeder Fehlermeldung; harmlose
  Meldungen blendet „Ignorieren“ für ein Gerät oder alle aus. Der Agent schickt seine Werte per HTTPS an den Server – auch hinter einer Firewall, ohne
  offene Ports beim Kunden-Server. Ist der Server kurz nicht erreichbar, puffert er und liefert nach.
- **SNMP-Geräte (v1, v2c, v3):** Switches (Ports, Verkehr, Fehler, LLDP-Nachbarn, PoE; Diagramme je Port),
  UniFi-Switches mit eigenem Profil (CPU, Arbeitsspeicher, Temperaturen, Lüfter, Netzteile, PoE-Budget), USV
  (Akku, Restlaufzeit, Last, Spannungen; RFC 1628 und APC), Drucker (Toner, Seitenzähler), NAS (Synology:
  Platten, RAID, Temperaturen) sowie CPU, Arbeitsspeicher und Speicher über HOST-RESOURCES-MIB und eigene OIDs.
- **USV über einen NUT-Server** (Network UPS Tools, z. B. UniFi UPS): Akku, Restlaufzeit, Last, Spannungen,
  Leistung, Temperaturen und Status (Netz-/Batteriebetrieb, Akku schwach, Akku tauschen, Überlast …).
- **Agent und API kombiniert:** Ein Agent lässt sich einem per API oder SNMP abgefragten Gerät zuordnen (z. B.
  einem Knoten des Proxmox-Clusters) – ein Gerät, alle Daten.
- **Netzwerk-Checks** für jedes Gerät: Ping (Antwortzeit, Verlust), HTTP(S) (Statuscode, Antwortzeit,
  Schlüsselwort), TLS-Zertifikat (Resttage), TCP-Port, DNS (Antwort, erwarteter Wert) und Domain-Ablauf (RDAP).
- **Übersichten:** Update-Stand aller Server, Windows-Ereignisse mit Regeln und Ausnahmen, Ablaufdaten aller Zertifikate und Domains, Verfügbarkeitsberichte
  (auch als Monatsbericht per E-Mail).
- **Alarme und Benachrichtigungen** per E-Mail, Telegram, ntfy, Gotify oder Webhook – mit Schwellwerten,
  Hysterese, Quittieren und Wartung – spontan oder als wiederkehrendes Wartungsfenster für Geräte und Ordner
  (z. B. monatlich zum Patchday).
- **Verlauf:** Diagramme bis zu einem Jahr (Rohwerte 2 Tage, 5-Minuten-Werte 35 Tage, Stundenwerte 400 Tage).
- **Externe API** (nur lesend, API-Token) und optional Push der Zusammenfassung an eine **Zentrale**, die alle
  Kunden-Container zusammenführt.

```
Docker-Host
└─ Container pk95monitoring (je Kunde einer)          Port 8443 (HTTPS, Weboberfläche + Agent-Endpunkte)
   ├─ /data                                           SQLite-Datenbank, Zertifikat, Einrichtungscode
   ├─ /opt/pk95monitoring/agents                      pk95agent für Linux amd64/arm64 und Windows amd64
   │
   ├─→ Proxmox VE      :8006  REST-API mit API-Token, Zertifikat per Fingerabdruck gepinnt
   ├─→ SNMP-Geräte     :161   Switch (auch UniFi), USV, Drucker, NAS
   ├─→ NUT-Server      :3493  USV (z. B. UniFi UPS)
   ├─→ Netzwerk-Checks        Ping, HTTP(S), TLS, TCP, DNS
   ├─← pk95agent              Linux-/Windows-Server melden sich alle 60 s per HTTPS
   └─→ Zentrale (optional)    Zusammenfassung per HTTPS-Push; oder die Zentrale fragt /api/v1 ab
```

## Schnellstart (Docker)

Voraussetzung: Docker mit Compose-Plugin auf einem Linux-Host (amd64 oder arm64), der die zu überwachenden Geräte
erreicht.

```bash
mkdir pk95monitoring && cd pk95monitoring
curl -fsSLO https://raw.githubusercontent.com/PhilippKuhlmann/pk95monitoring/main/docker-compose.yml
# PK95MON_INSTANCE (Kundenname) in docker-compose.yml anpassen
docker compose up -d
docker compose logs pk95monitoring      # Einrichtungscode
```

1. `https://<host>:8443` öffnen (selbst signiertes Zertifikat), Einrichtungscode eingeben, Konto anlegen.
   Der Code steht im Log (Zeile `setup code: …`) und in `/data/setup-code`
   (`docker compose exec pk95monitoring cat /data/setup-code`).
2. **Geräte → Gerät hinzufügen:** Proxmox VE, SNMP-Gerät oder Netzwerk-Checks.
3. **Agents:** Befehl kopieren und auf den Linux- und Windows-Servern ausführen.
4. **Einstellungen:** Benachrichtigungen, Schwellwerte, bei Bedarf API-Token und Zentrale.

Das Image läuft als Benutzer `10001:10001`. Das benannte Volume aus der `docker-compose.yml` übernimmt die
Rechte von `/data` automatisch. Wer stattdessen ein Verzeichnis einbindet (`./data:/data`), legt es vorher an:
`mkdir -p data && sudo chown 10001:10001 data`.

Ping-Checks laufen ohne Root-Rechte über ICMP-Sockets; dafür setzt die Compose-Datei
`net.ipv4.ping_group_range: "0 65535"`. Der Bereich deckt alle Gruppen im Container ab und funktioniert auch in
einem unprivilegierten LXC-Container (z. B. Docker in einem Proxmox-Container): Dort sind nur 65536 IDs
zugeordnet, der größere Bereich `"0 2147483647"` lässt sich nicht setzen und der Container startet nicht
(`write … ping_group_range: invalid argument`). Mit `network_mode: host` ist die Einstellung nicht erlaubt – dort
muss der Wert auf dem Host gesetzt sein.

Der Container hat einen eingebauten Healthcheck (`docker ps` zeigt `healthy`); er fragt `/api/session` auf dem
eigenen Port ab und beachtet `PK95MON_LISTEN` und `PK95MON_NO_TLS`.

### Docker in einem Proxmox-LXC-Container

Docker läuft auch in einem unprivilegierten LXC-Container. Dafür am Container die Optionen **nesting** und
**keyctl** einschalten (Oberfläche: Container → Optionen → Funktionen, oder auf dem Proxmox-Host
`pct set <CTID> --features nesting=1,keyctl=1`) und den Container neu starten. `ping_group_range` muss dann wie
in der Compose-Datei `"0 65535"` sein (siehe oben).

### Image selbst bauen

`docker compose up -d` lädt das fertige Image von `ghcr.io`. Aus einem Checkout des Repositorys baut
`docker compose up -d --build` das Image selbst. Das Dockerfile braucht BuildKit (Cache-Mounts, Heredoc): Docker
Desktop und die Pakete `docker-ce` von Docker bringen es mit; mit dem Debian-Paket `docker.io` zusätzlich
`apt install docker-buildx` – sonst bricht der Build mit dem alten Builder ab.

## Ein Container je Kunde

Jeder Kunde bekommt einen eigenen Container mit eigenem Volume und eigenem Port – Daten, Zugänge,
Benachrichtigungen und Agents sind so sauber getrennt. Beispiel für zwei Kunden auf einem Host:

```yaml
services:
  kunde-a:
    image: ghcr.io/philippkuhlmann/pk95monitoring:latest
    container_name: pk95mon-kunde-a
    restart: unless-stopped
    ports: ["8444:8443"]
    volumes: ["kunde-a-data:/data"]
    environment:
      TZ: Europe/Berlin
      PK95MON_INSTANCE: "Kunde A GmbH"
      PK95MON_PUBLIC_URL: "https://monitor.example.de:8444"
    sysctls:
      net.ipv4.ping_group_range: "0 65535"
  kunde-b:
    image: ghcr.io/philippkuhlmann/pk95monitoring:latest
    container_name: pk95mon-kunde-b
    restart: unless-stopped
    ports: ["8445:8443"]
    volumes: ["kunde-b-data:/data"]
    environment:
      TZ: Europe/Berlin
      PK95MON_INSTANCE: "Kunde B KG"
      PK95MON_PUBLIC_URL: "https://monitor.example.de:8445"
    sysctls:
      net.ipv4.ping_group_range: "0 65535"
volumes:
  kunde-a-data:
  kunde-b-data:
```

## Hinter einem Reverse Proxy (empfohlen mit eigenem Zertifikat)

Übernimmt ein Reverse Proxy wie Caddy (oder Traefik, nginx …) TLS mit einem richtigen Zertifikat, ist das der
empfohlene Aufbau: Der Container spricht dann nur HTTP, der Port ist nur lokal erreichbar, und Browser wie Agents
sehen ein gültiges Zertifikat (kein Fingerabdruck nötig).

```yaml
    ports: ["127.0.0.1:8080:8080"]
    environment:
      PK95MON_NO_TLS: "1"
      PK95MON_LISTEN: ":8080"
      PK95MON_PUBLIC_URL: "https://monitor.example.de"
```

```
# Caddyfile
monitor.example.de {
	reverse_proxy 127.0.0.1:8080
}
```

Der Proxy reicht alles weiter, auch `/agent/` (Installation, Downloads und Meldungen der Agents).
`PK95MON_PUBLIC_URL` ist die Adresse, die in den Agent-Befehlen steht. `X-Forwarded-For`/`-Proto`/`-Host` wertet
der Server nur von lokalen bzw. privaten Adressen aus (wie sie ein Proxy auf demselben Host oder im selben Netz
hat). Ein vollständiges Beispiel steht auskommentiert in der `docker-compose.yml` (`kunde-c`).

## Umgebungsvariablen

| Variable | Standard (Image) | Bedeutung |
|---|---|---|
| `PK95MON_LISTEN` | `:8443` | HTTPS (mit `PK95MON_NO_TLS=1` HTTP) |
| `PK95MON_HTTP_REDIRECT` | leer | zusätzlicher HTTP-Port, der auf HTTPS umleitet (leer = aus) |
| `PK95MON_DATA_DIR` | `/data` | SQLite-Datenbank, `cert.pem`/`key.pem`, Einrichtungscode |
| `PK95MON_AGENT_DIR` | `/opt/pk95monitoring/agents` | Agent-Dateien `pk95agent-linux-amd64`, `pk95agent-linux-arm64`, `pk95agent-windows-amd64.exe` |
| `PK95MON_PUBLIC_URL` | leer | Adresse, unter der die Agents den Server erreichen; hat Vorrang vor der Einstellung (beide leer = aus der Browser-Anfrage). Ihr Hostname kommt auch ins selbst erzeugte Zertifikat |
| `PK95MON_INSTANCE` | leer | Instanzname bzw. Kunde; sonst in den Einstellungen, die Variable hat Vorrang |
| `PK95MON_DEMO` | leer | `1` = simulierte Geräte (Vorführung, Entwicklung) |
| `PK95MON_NO_TLS` | leer | `1` = nur HTTP (hinter einem Reverse Proxy); `PK95MON_HTTP_REDIRECT` entfällt dann |
| `TZ` | UTC | Zeitzone, z. B. `Europe/Berlin` |

Für `PK95MON_DEMO` und `PK95MON_NO_TLS` gelten `1`, `true`, `yes` und `on` als eingeschaltet.

Eigenes Zertifikat: `cert.pem` und `key.pem` im Volume (`/data`) ersetzen und den Container neu starten. Ist es
selbst signiert, pinnen die Agent-Befehle es per Fingerabdruck (Seite **Agents**); ein Zertifikat einer
öffentlichen CA prüfen curl, PowerShell und der Agent ganz normal.

## Proxmox VE

Auf einem Proxmox-Knoten als `root` in der Shell einen Benutzer mit Leserechten und ein API-Token anlegen:

```bash
pveum user add pk95mon@pve --comment "PK95 Monitoring"
pveum acl modify / --users pk95mon@pve --roles PVEAuditor
pveum user token add pk95mon@pve monitoring --privsep 0
```

Token-ID `pk95mon@pve!monitoring` und Secret („value“, wird nur einmal angezeigt) unter **Geräte → Gerät
hinzufügen → Proxmox VE** eintragen, Fingerabdruck mit „Abrufen“ holen und mit dem in Proxmox vergleichen.
`PVEAuditor` genügt für Cluster, Knoten, Gäste, Speicher, ZFS, SMART und Sicherungen. Ein Knoten genügt, auch bei
einem Cluster.

Optional für die **Update-Prüfung**: Proxmox verlangt dafür das Recht `Sys.Modify` auf `/nodes`. Es erlaubt auch
Änderungen am Knoten (z. B. Paketquellen aktualisieren) – deshalb nur, wenn die Update-Checks gewünscht sind:

```bash
pveum role add PK95MonUpdates --privs Sys.Modify
pveum acl modify /nodes --users pk95mon@pve --roles PK95MonUpdates
```

Ohne dieses Recht entfallen die Update-Checks; der Knoten zeigt dann einen Hinweis.

Welche Gäste laufen müssen, steht im Gerät: „Beim Booten starten“ (Standard), alle oder keine; einzelne VMIDs
(Vorlagen, Testmaschinen) lassen sich ausnehmen.

## Agent (Linux und Windows)

Die Seite **Agents** zeigt die fertigen Befehle mit Anmeldeschlüssel, Fingerabdruck und SHA-256-Prüfsummen. Das
Skript lädt den Agent vom Server, prüft die Prüfsumme, meldet ihn an und richtet den Dienst ein. Nach etwa einer
Minute steht der Server in der Liste.

**Linux** (Debian, Ubuntu, RHEL-Familie, Proxmox-Hosts …; amd64 und arm64, systemd) als root oder mit sudo:

```bash
curl -fsSL -k --pinnedpubkey 'sha256//…' 'https://monitor.example.de:8443/agent/install.sh' | $(command -v sudo) bash -s -- --key '…' --fingerprint … --sha256-amd64 … --sha256-arm64 …
```

Mit dem selbst erzeugten Zertifikat lädt curl das Skript nur von genau diesem Server (`--pinnedpubkey`), das
Skript prüft den Agent per SHA-256, und der Agent pinnt den Fingerabdruck. Mit einem CA-Zertifikat (z. B. hinter
Caddy) fallen `-k`, `--pinnedpubkey` und `--fingerprint` weg.

**Windows** (Server 2016 und neuer, Windows 10/11; 64 Bit) in einer Eingabeaufforderung oder PowerShell „Als
Administrator“:

```bat
powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "[Net.ServicePointManager]::SecurityProtocol=[Net.SecurityProtocolType]::Tls12; [Net.ServicePointManager]::ServerCertificateValidationCallback={ … -eq '…' }; & ([scriptblock]::Create((New-Object Net.WebClient).DownloadString('https://monitor.example.de:8443/agent/install.ps1'))) -Key '…' -Fingerprint '…' -Sha256 '…'"
```

Der Rückruf akzeptiert nur das Zertifikat mit dem angezeigten Fingerabdruck; mit einem CA-Zertifikat entfällt er
samt `-Fingerprint`.

**Von Hand:** Datei unter **Agents → Agent-Dateien** laden (oder aus dem GitHub-Release) und

```bash
pk95agent install --server https://monitor.example.de:8443 --key SCHLÜSSEL --fingerprint SHA256
```

| Befehl | |
|---|---|
| `pk95agent install --server URL --key SCHLÜSSEL [--fingerprint SHA256] [--insecure] [--device GERÄTE-ID [--node KNOTEN]]` | anmelden, Konfiguration schreiben, Dienst einrichten und starten; mit `--device` einem vorhandenen Gerät zuordnen (siehe unten) |
| `pk95agent uninstall` | Dienst, Konfiguration und Zwischenspeicher entfernen (unter Linux auch das Programm) |
| `pk95agent once` | einen Bericht erfassen und als JSON ausgeben (Fehlersuche) |
| `pk95agent run` | im Vordergrund laufen (so startet ihn auch der Dienst) |
| `pk95agent version` | Version anzeigen |

Alle Befehle verstehen `--config PFAD` (Standard: die Konfigurationsdatei unten).

- Programm: `/usr/local/bin/pk95agent` bzw. `C:\Program Files\PK95Agent\pk95agent.exe`
- Konfiguration: `/etc/pk95agent/agent.json` bzw. `C:\ProgramData\PK95Agent\agent.json`
- Der Agent prüft das Server-Zertifikat anhand des Fingerabdrucks – auch wenn es selbst signiert ist; ohne
  Fingerabdruck (CA-Zertifikat) über die Zertifizierungsstellen des Systems.
- Welche Dienste laufen müssen, Intervall und Schwellwerte stellst du auf der Seite des Servers ein; der Agent
  holt sich die Einstellungen mit jeder Meldung.
- **Updates:** Mit „Agents automatisch aktualisieren“ (Einstellungen → Agents, Standard an) ersetzen sich ältere Agents selbst
  durch die Version des Servers – nach Prüfung der SHA-256-Prüfsumme.
- Ein neuer Anmeldeschlüssel (Agents → „Neu erzeugen“) sperrt nur neue Installationen; angemeldete Agents haben
  eigene Zugangsdaten.

### Agent und API kombinieren

Ein Server, der schon per API oder SNMP überwacht wird – z. B. ein Knoten des Proxmox-Clusters –, kann
zusätzlich den Agent bekommen. Dann bleibt es **ein Gerät**: Die Agent-Daten (Festplatten, Dienste, systemd,
Updates …) ergänzen die API-Daten. Bei einem Proxmox-Cluster gehört jeder Agent zu einem Knoten. Das geht für alle
Gerätearten (Proxmox, SNMP, Netzwerk-Checks).

- **Neu installieren:** Am Gerät „Agent hinzufügen“ → „Neu installieren“ (bei Proxmox den Knoten wählen). Der
  angezeigte Befehl enthält zusätzlich `--device <Geräte-ID> --node <Knoten>` (Windows: `-Device … -Node …`);
  der Agent wird bei der Anmeldung gleich zugeordnet. Ohne `--node` nimmt der Server bei Proxmox den Knoten mit
  dem Namen des Hosts (Kurzname, ohne Groß-/Kleinschreibung).
- **Vorhandenen Agent zuordnen:** „Agent hinzufügen“ → „Vorhandenen Agent zuordnen“. Passt der Hostname oder eine
  IP-Adresse eines Agents zu einem Gerät oder Proxmox-Knoten, schlägt die Seite des Agents die Zuordnung vor
  („Gehört dieser Server zu …?“).
- **Anzeige:** Listen und Übersicht zeigen nur das Gerät (mit „+1 Agent“); sein Zustand ist der schlechteste aus
  Gerät und Agents, die Meldung nennt den Knoten (z. B. „pve1: …“). Auf der Geräteseite steht der Agent beim
  Knoten; der Knoten zeigt den Agent als Fakt. Der Agent behält seine eigene Seite, Checks, Schwellwerte, Regeln,
  Alarme und Diagramme (Alarme heißen „Gerät / Knoten“, z. B. „Proxmox-Cluster / pve1“).
- **Lösen:** Auf der Seite des Agents „Lösen“ – er ist dann wieder ein eigenes Gerät. Wird das Gerät gelöscht,
  werden seine Agents ebenfalls eigenständig.

## SNMP

Auf dem Gerät SNMP einschalten und den Monitoring-Host als Abfrager zulassen (UDP 161).

- **v1/v2c:** Community eintragen (Standard `public`, besser eine eigene, nur lesend).
- **v3:** Benutzer, Sicherheitsstufe (`noAuthNoPriv`, `authNoPriv`, `authPriv`), Authentifizierung (MD5, SHA,
  SHA224 … SHA512) und Verschlüsselung (DES, AES, AES192, AES256) mit Passwörtern.
- **Profil:** „Automatisch“ erkennt Switch, UniFi-Switch, USV, Drucker und NAS an `sysObjectID`/`sysDescr`;
  sonst fest wählen.
- „Verbindung testen“ liest Name, Beschreibung und Schnittstellen.
- **Ports:** Alle Schnittstellen eines Switches werden angezeigt (ohne Loopback, höchstens 512). Ein Port ohne
  Link ist normal und bleibt OK („Kein Link“, abgeschaltet: „Deaktiviert“) – Ports dürfen offline gehen. Nur die
  unter **„Alarm bei Link-Ausfall“** ausgewählten Ports (Uplinks, Server, NAS …) werden kritisch, wenn sie den
  Link verlieren. Verkehr und Fehler werden für alle Ports mit Link aufgezeichnet.
- **Eigene Werte:** beliebige numerische OIDs mit Name, Einheit und Faktor.
- **Switches** (Profil „Switch“ und „UniFi-Switch“): LLDP-Nachbarn je Port („Nachbar“, „Nachbar-Port“) und PoE
  (LLDP-MIB, POWER-ETHERNET-MIB): Leistung je Port, PoE-Klasse, Fehler, Gesamtverbrauch und Budget (Warnung ab
  85 %, kritisch ab 95 % Auslastung). Die CPU-Schnittstelle und unbenutzte virtuelle Schnittstellen werden
  ausgeblendet.
- **Diagramme je Port:** In der Port-Liste öffnet „Diagramme“ (oder ein Klick auf die Zeile) Empfangen/Gesendet,
  Fehler und PoE-Leistung des Ports für 1 Stunde bis 1 Jahr.

### UniFi-Switches

Das Profil „UniFi-Switch“ kennt die OIDs der UniFi-Switches (Broadcom FASTPATH, wie EdgeSwitch) und liest
zusätzlich Modell, Firmware, Seriennummer, CPU, Arbeitsspeicher, Temperaturen, Lüfter, Netzteile und die
PoE-Leistung je Port. „Automatisch“ erkennt UniFi-Switches an `sysDescr` (z. B. `USW-Pro-24-PoE, 7.0.50…`)
bzw. `sysObjectID`; nur Modelle, die sich als reines `Linux … mips` melden, muss man von Hand auf
„UniFi-Switch“ stellen.

SNMP einschalten (laut Recherche, nicht an einem Controller geprüft): In **UniFi Network** wird SNMP **global für
alle adoptierten Geräte** eingestellt (Einstellungen, Bereich SNMP-Überwachung): v1/v2c mit Community oder v3 mit
Benutzer. Der Controller kann eine von Hand auf dem Switch gesetzte SNMP-Konfiguration beim nächsten
Provisionieren überschreiben – deshalb im Controller einstellen.

Grenzen (ebenfalls laut Recherche): Kleine Gen2-Modelle ohne „Pro“ (z. B. USW-24-PoE, USW-Lite) liefern über SNMP
oft nur System, CPU und Arbeitsspeicher, teils ohne Switch-Ports, Temperaturen, Lüfter, PoE oder LLDP.
USW-Flex-Mini und die Ultra-Reihe haben kein SNMP. Die Leistung je PoE-Port kommt aus einer Herstellertabelle,
die auf UniFi nicht bestätigt ist – fehlt sie, zeigt der Port nur „aktiv“ ohne Watt. PoE-Klassen über 4 (802.3bt)
kann SNMP nicht melden.

## USV über NUT (z. B. UniFi UPS)

Viele USV werden über einen **NUT-Server** (Network UPS Tools, `upsd`, TCP-Port 3493) abgefragt – z. B. eine
UniFi UPS, ein Synology-/QNAP-NAS oder ein Raspberry Pi mit angeschlossener USV. Unter **Geräte → Gerät
hinzufügen → USV (NUT-Server)** Host, Port (Standard 3493) und optional UPS-Name, Benutzer und Passwort eintragen
(nur nötig, wenn der Server eine Anmeldung zum Lesen verlangt). „Verbindung testen“ listet die USV des Servers;
ein Klick übernimmt den Namen. UPS-Name leer = jede USV des Servers wird überwacht.

Je USV gibt es einen Check „USV <Name>“ (bzw. die Beschreibung vom Server; Gruppe USV) mit Ladung, Restlaufzeit,
Last, Ein-/Ausgangsspannung, Leistung, Akkuspannung und Temperaturen sowie Hersteller, Modell, Seriennummer,
Firmware und Akku-Datum. Zustand aus `ups.status`: Netzbetrieb = OK; Batteriebetrieb, „Akku tauschen“ und Bypass
= Warnung; „Akku schwach“, Überlast, ausgeschaltet und „Wird heruntergefahren“ = kritisch. Schwellwerte wie bei
der SNMP-USV: Ladung unter 50 % Warnung / 20 % kritisch, Restlaufzeit unter 10 / 5 Minuten. Ist der NUT-Server
nicht erreichbar, wird die Erreichbarkeit kritisch.

**UniFi UPS** (nicht an einem Gerät geprüft): In UniFi Network bei der USV die Bereitstellung per NUT
(„NUT-Server“) einschalten und – falls angeboten – Benutzer und Passwort vergeben. Dann die IP-Adresse des Geräts,
das den NUT-Server bereitstellt, als Host eintragen und mit „Verbindung testen“ den UPS-Namen ermitteln. Port
3493/TCP muss vom Monitoring-Host aus erreichbar sein.

## Was wird überwacht? – Checks auswählen

Jedes Gerät liefert seine Checks (Ports, Festplatten, VMs, Dienste, Updates …). Am Gerät legst du unter
„Checks auswählen“ für jeden Check – oder für viele auf einmal per Muster – fest, was damit passiert:

| Auswahl | Wirkung |
|---|---|
| **Überwachen** (Standard) | Zustand zählt, Warnung/Kritisch öffnet einen Alarm mit Benachrichtigung. |
| **Nur anzeigen** | Wird abgefragt, angezeigt und aufgezeichnet (Diagramme), aber nie gelb oder rot und nie alarmiert. Ein Wert, der sonst Warnung/Kritisch wäre, ist als „stumm“ markiert. |
| **Ausblenden** | Der Check verschwindet samt Messwerten; es wird nichts gespeichert. Ausgeblendete Checks stehen am Gerät in einer eigenen Liste und lassen sich dort wieder einblenden (sie erscheinen bei der nächsten Abfrage). |

Eine Regel nennt einen Check-Schlüssel (`snmp:if:Gi1/0/1`) oder ein Muster mit `*` für beliebigen Text:

| Muster | trifft |
|---|---|
| `snmp:if:*` | alle Switch-Ports |
| `snmp:if:Gi1/0/2*` | Ports Gi1/0/2 und Gi1/0/20 … Gi1/0/29 |
| `pve:vm:*` | alle VMs eines Proxmox-Clusters (`pve:ct:*` alle Container) |
| `pve:updates:*` | Updates aller Proxmox-Knoten |
| `disk:/boot*` | `/boot` und `/boot/efi` eines Agents |
| `svc:*` | alle Dienste eines Agents |
| `*` | alle Checks des Geräts |

- Eine Regel mit genauem Schlüssel gewinnt immer gegen Muster, unter mehreren Mustern das längste. Beispiel:
  `pve:vm:*` = Nur anzeigen und `pve:vm:100` = Überwachen – nur VM 100 alarmiert.
- Die Änderung wirkt sofort: Ein offener Alarm eines Checks, der auf „Nur anzeigen“ oder „Ausblenden“ gestellt
  wird, endet ohne „Wieder OK“-Nachricht (Ereignis „… wird nicht mehr alarmiert“ bzw. „… ausgeblendet“).
  Zurück auf „Überwachen“ alarmiert sofort, wenn der Check noch schlecht ist.
- Die **Erreichbarkeit** lässt sich nicht abwählen; für ein Gerät ganz ohne Alarme gibt es „Keine Alarme“.
- **Switch:** Alle Ports überwachen, aber Ports dürfen offline gehen – das ist der Standard. Für Ports, deren
  Ausfall zählt, „Alarm bei Link-Ausfall“ in der SNMP-Konfiguration wählen; unwichtige Ports (z. B.
  `snmp:if:vlan*`) bei Bedarf ausblenden.

## Schwellwerte und Alarme

Jeder Check hat einen Zustand: OK, Warnung, Kritisch oder Unbekannt. Gemessene Werte werden mit Schwellwerten
verglichen; Reihenfolge: Gerät mit Instanz (z. B. `disk.usage:/var`) → Gerät → Einstellungen mit Instanz →
Einstellungen → Standard.

| Wert | Warnung | Kritisch | Hysterese |
|---|---|---|---|
| `cpu.usage` – CPU-Auslastung | ≥ 90 % | ≥ 98 % | 5 Werte in Folge |
| `mem.usage` – Arbeitsspeicher | ≥ 90 % | ≥ 97 % | 3 Werte in Folge |
| `disk.usage` – Festplatte | ≥ 85 % | ≥ 95 % | sofort |
| `storage.usage` – Proxmox-Speicher | ≥ 85 % | ≥ 95 % | sofort |
| `ping.loss` – Paketverlust | ≥ 20 % | 100 % | 2 Werte in Folge |
| `http.time` – Antwortzeit | ≥ 5000 ms | ≥ 15000 ms | 2 Werte in Folge |
| `cert.days` – Zertifikat gültig | ≤ 21 Tage | ≤ 7 Tage | sofort |
| `ups.battery` – USV-Akku | ≤ 50 % | ≤ 20 % | sofort |
| `ups.runtime` – USV-Restlaufzeit | ≤ 10 min | ≤ 5 min | sofort |
| `printer.toner` – Toner | ≤ 10 % | ≤ 2 % | sofort |
| `poe.usage` – PoE-Budget eines Switches | ≥ 85 % | ≥ 95 % | sofort |
| `updates.security` – Sicherheitsupdates | ≥ 1 | – | sofort |

- **Hysterese:** Ein Schwellwert schlägt erst nach der angegebenen Zahl schlechter Werte in Folge an; zurück auf
  OK geht es sofort. Zustände der Geräte selbst (Dienst gestoppt, VM aus, Port down) gelten sofort.
- **Erreichbarkeit:** Schlägt die Abfrage zweimal in Folge fehl, ist das Gerät kritisch. Ein Agent gilt als
  ausgefallen, wenn er sich `max(3 × Intervall, 180 s)` nicht meldet.
- **Alarme:** Wird ein Check Warnung oder Kritisch, öffnet sich ein Alarm mit Benachrichtigung; steigt er auf
  Kritisch, kommt eine weitere. Zurück auf OK schließt den Alarm („Wieder OK“). Offene, nicht quittierte Alarme
  können alle N Stunden erneut melden (Einstellungen → Allgemein).
- **Wartung:** Am Gerät „Wartung“ starten (Dauer oder bis Zeitpunkt, mit Notiz) – bis dahin keine Alarme und
  Benachrichtigungen, die Werte werden weiter aufgezeichnet. „Keine Alarme“ am Gerät schaltet sie dauerhaft ab.

## Benachrichtigungen

Unter **Einstellungen → Benachrichtigungen**, mit „Melden ab“ (alles inkl. „Wieder OK“, Warnungen oder nur
kritische Alarme) und „Testnachricht senden“:

- **E-Mail** über SMTP (STARTTLS, TLS oder unverschlüsselt).
- **Telegram:** Bot bei @BotFather anlegen (`/newbot`), dem Bot schreiben oder ihn in die Gruppe aufnehmen, dann
  „Chat-ID ermitteln“.
- **ntfy**, **Gotify** oder **Webhook (JSON)** mit URL und optionalem Token.

## Externe API und Zentrale

Für eine zentrale Übersicht über alle Kunden-Container: unter **Einstellungen → API-Token** ein Token erstellen
(wird nur einmal angezeigt, beginnt mit `pk95_`). Die API ist nur lesend:

| Endpunkt | Inhalt |
|---|---|
| `GET /api/v1/summary` | Instanz, Version, Gesamtzustand, Zähler, alle Geräte, aktive Alarme |
| `GET /api/v1/devices` | `{"devices": [...]}` |
| `GET /api/v1/devices/{id}` | `{"device": {...}, "checks": [...]}` mit allen Checks des Geräts |
| `GET /api/v1/alerts?state=active` | `{"alerts": [...]}` (`state=all` mit beendeten, neueste zuerst; `limit` bis 1000, Standard 100, `before` zum Blättern) |

Höchstens 60 Anfragen pro Minute und Absender (sonst 429).

```bash
curl -fsS -k -H "Authorization: Bearer pk95_…" https://monitor.example.de:8443/api/v1/summary
```

```json
{
  "instance": "Kunde Muster GmbH",
  "version": "1.0.0",
  "url": "https://monitor.example.de:8443",
  "time": 1762887600,
  "state": "warn",
  "counts": { "ok": 11, "warn": 1, "crit": 0, "unknown": 0, "maintenance": 1, "disabled": 0 },
  "devices": [
    {
      "id": "k3x9q2", "name": "srv-dc01", "type": "agent", "group": "Server", "host": "10.0.0.10",
      "enabled": true, "maintenance": false, "state": "warn", "message": "Festplatte C: 91 % ≥ 85 %",
      "last_seen": 1762887590, "checks": { "ok": 14, "warn": 1, "crit": 0, "unknown": 0 }
    }
  ],
  "alerts": [
    {
      "id": 42, "device_id": "k3x9q2", "device_name": "srv-dc01", "check_key": "disk:C:",
      "check_name": "Festplatte C:", "severity": "warn", "message": "Festplatte C: 91 % ≥ 85 %",
      "started": 1762880000
    }
  ]
}
```

Zeiten sind Unix-Sekunden. **Push zur Zentrale** (Einstellungen → Zentrale), wenn die Zentrale den Container nicht
erreicht: alle N Minuten (Standard 5) geht dieselbe Zusammenfassung per `POST` an die eingetragene URL, mit
`Authorization: Bearer <Token>`. Schlägt das fehl, steht einmalig eine Warnung unter Ereignisse.

## Sicherung und Update

Alles Wichtige liegt im Volume `/data` (Datenbank mit Geräten, Einstellungen, Verlauf, Alarmen; Zertifikat).
Sicherung, z. B. nachts:

```bash
docker compose stop pk95monitoring
docker run --rm -v pk95monitoring_pk95mon-data:/data -v "$PWD":/backup alpine \
  tar czf /backup/pk95mon-$(date +%F).tar.gz -C /data .
docker compose start pk95monitoring
```

Der Volume-Name ist `<Projektordner>_pk95mon-data` (`docker volume ls`). Bei einem eingebundenen Verzeichnis
(`./data`) genügt es, dieses bei gestopptem Container zu kopieren. Wiederherstellen: Archiv in ein leeres Volume
entpacken, Rechte `10001:10001`.

Update:

```bash
docker compose pull && docker compose up -d
```

Die Datenbank wird beim Start automatisch migriert. Die Agents ziehen mit automatischen Updates selbst nach.

## Sicherheit

- HTTPS mit selbst erzeugtem Zertifikat (`/data/cert.pem`, `key.pem` – durch eigene ersetzbar).
- Anmeldung mit Sitzungscookie, ändernde Anfragen brauchen einen eigenen Header (CSRF-Schutz).
- Proxmox und Agents pinnen Zertifikate per **SHA-256-Fingerabdruck**; Agent-Downloads werden per SHA-256 geprüft.
- Secrets (Proxmox-Token, SNMP-Community und -Passwörter, NUT-Passwort, Benachrichtigungs-Token) werden nie an den Browser
  zurückgegeben. API- und Agent-Tokens liegen nur als Hash in der Datenbank.
- Der Container läuft ohne Root-Rechte. Port 8443 nur dort freigeben, wo Agents und Benutzer ihn brauchen.

## Entwicklung

```bash
make test        # go test -race ./... (Proxmox VE, SNMP und NUT simuliert: internal/fake)
make check       # go vet (auch für die Linux-/Windows-Dateien des Agents), gofmt, node --check web/app.js
make demo        # Agents bauen, Demo auf http://127.0.0.1:18096 (Code in data/demo/setup-code)
make agents      # dist/agents/pk95agent-{linux-amd64,linux-arm64,windows-amd64.exe}
make build       # Server ./pk95monitoring
make docker      # Multi-Arch-Image (DOCKER_FLAGS=--push, oder PLATFORMS=linux/amd64 DOCKER_FLAGS=--load)
```

Der Demo-Modus legt einen simulierten Proxmox-Cluster (3 Knoten, Gäste, Speicher, eine fehlgeschlagene
Sicherung), SNMP-Geräte (Switch, UniFi-PoE-Switch, USV, Drucker, NAS), eine UniFi UPS am NUT-Server, fünf Agents
(Debian, Windows; einer gehört zum Knoten pve1 des Clusters) und Netzwerk-Checks an und füllt sieben Tage Verlauf
vor (auch für Demo-Geräte, die eine neuere Version zu einer bestehenden Demo-Datenbank hinzufügt). Architektur, Check-Schlüssel, Metriken und die REST-Schnittstelle: `DESIGN.md`.

CI (`.github/workflows/release.yml`) prüft jeden Push und Pull Request: `make check`, Tests, Agent-Cross-Build und
einen Build des Images. Release: Tag `vX.Y.Z` pushen – dann schiebt der Workflow das Image
`ghcr.io/<owner>/pk95monitoring` für amd64 und arm64 (Tags `X.Y.Z`, `X.Y` und `latest`) und hängt die
Agent-Dateien mit `SHA256SUMS` an das GitHub-Release.
