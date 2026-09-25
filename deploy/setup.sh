#!/usr/bin/env bash
#
# Abys-Invest — deploy/setup.sh
# Instalación idempotente y segura del API Go + dashboard estático bajo systemd
# (plan M4, D4/T5). Debe ejecutarse como root:
#
#     sudo bash deploy/setup.sh              # modo estático (default, M4)
#     sudo bash deploy/setup.sh --with-air   # variante Air live-reload (M4d)
#     sudo bash deploy/setup.sh --help       # ayuda
#
# Sin `--with-air` el flujo es IDÉNTICO al de M4 (binario estático). Con
# `--with-air` además: copia las fuentes Go a /opt/abys-invest/src, instala Air
# en /usr/local/bin/air, copia .air.prod.toml y habilita la unit alterna
# abys-invest-api-air.service (recompila+reinicia ante cambios). Guardas
# extra en modo air: aborta si falta la toolchain Go o si secrets.env sigue con
# DATABASE_URL placeholder.
#
# Guardas de seguridad:
#   - NO sobrescribe secrets.env si ya existe; genera un PLACEHOLDER que el
#     operador edita (nunca se escriben secretos reales).
#   - NO habilita / arranca el servicio mientras secrets.env no contenga una
#     DATABASE_URL real (no-placeholder) — guard de enable.
#   - Preserva bin/api.prev antes de instalar el binario nuevo; ante fallo de
#     arranque restaura el binario previo y reinicia (rollback).
#   - Health check final con timeout corto y mensajes accionables; solo se
#     ejecuta si el servicio fue realmente arrancado por este script.
set -euo pipefail

# ── Configuración (overridable vía entorno) ──────────────────────────────────
INSTALL_DIR="${INSTALL_DIR:-/opt/abys-invest}"
SECRETS_DIR="${SECRETS_DIR:-/etc/abys-invest}"
SECRETS="${SECRETS:-${SECRETS_DIR}/secrets.env}"

# Fuentes relativas al script (no al cwd): permiten `sudo bash deploy/setup.sh`
# desde cualquier directorio y con sudoers que fijen HOME/cwd/rundir.
_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
_REPO_ROOT="$(cd "${_SCRIPT_DIR}/.." && pwd)"
BIN_SOURCE="${BIN_SOURCE:-${_REPO_ROOT}/bin/api}"
DIST_SOURCE="${DIST_SOURCE:-${_REPO_ROOT}/web/dist}"

INSTALL_USER="${INSTALL_USER:-abys}"
INSTALL_GROUP="${INSTALL_GROUP:-abys}"
BIN_DEST="${INSTALL_DIR}/bin/api"
BIN_PREV="${BIN_DEST}.prev"
HEALTH_TIMEOUT_S="${HEALTH_TIMEOUT_S:-10}"
START_GRACE_S="${START_GRACE_S:-15}"

# Air (modo --with-air): binario que instala setup.sh y que espera la unit air.
AIR_BIN="${AIR_BIN:-/usr/local/bin/air}"
AIR_CONFIG_DEST="${AIR_CONFIG_DEST:-${INSTALL_DIR}/.air.prod.toml}"

log() { printf '[setup.sh] %s\n' "$*"; }
die() { printf '[setup.sh] ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
    cat <<'EOF'
Uso: sudo bash deploy/setup.sh [--with-air] [--help]

Instala el API Go + dashboard estático de Abys-Invest como servicio systemd.

Sin argumentos (default — modo estático):
    instala bin/api (de 'make build'), web/dist y la unit abys-invest-api.service
    (binario estático; se actualiza con un re-deploy manual).

--with-air (variante Air live-reload, unit ALTERNA):
    además copia las fuentes Go (cmd/, internal/, go.mod, go.sum) a
    /opt/abys-invest/src, instala Air (go install ...@latest → /usr/local/bin/air),
    copia deploy/.air.prod.toml a /opt/abys-invest/.air.prod.toml y habilita la
    unit abys-invest-api-air.service (air recompila y reinicia el API al
    detectar cambios). Guardas: aborta si falta la toolchain Go (which go) o si
    secrets.env sigue con DATABASE_URL placeholder.

--help: muestra esta ayuda.

Secretos: /etc/abys-invest/secrets.env (placeholder al generarse; el servicio
solo se habilita cuando DATABASE_URL es real).
EOF
}

# ── Argumentos ───────────────────────────────────────────────────────────────
WITH_AIR=0
for _arg in "$@"; do
    case "${_arg}" in
        --with-air) WITH_AIR=1 ;;
        --help | -h) usage; exit 0 ;;
        *) die "argumento desconocido: '${_arg}' (usa --help)" ;;
    esac
done

# Servicio y unit efectivos según el modo (respetan overrides por entorno).
if [[ "${WITH_AIR}" == "1" ]]; then
    : "${SERVICE:=abys-invest-api-air}"
    : "${UNIT_SRC:=${_REPO_ROOT}/deploy/abys-invest-api-air.service}"
    : "${AIR_CONFIG_SRC:=${_REPO_ROOT}/deploy/.air.prod.toml}"
    HEALTH_URL="${HEALTH_URL:-http://localhost:8082/health}"
else
    : "${SERVICE:=abys-invest-api}"
    : "${UNIT_SRC:=${_REPO_ROOT}/deploy/abys-invest-api.service}"
    HEALTH_URL="${HEALTH_URL:-http://localhost:8080/health}"
fi

# ── Precondiciones ───────────────────────────────────────────────────────────
[[ "$(id -u)" -eq 0 ]] || die "ejecutar como root: sudo bash ${0}"
command -v systemctl >/dev/null 2>&1 || die "systemd/systemctl no disponible"
# En modo air el artefacto son las fuentes (air compila en el arranque): no se
# exige bin/api prebuilt. En modo estático sí (lo produce 'make build').
if [[ "${WITH_AIR}" != "1" && ! -f "${BIN_SOURCE}" ]]; then
    die "binario no encontrado: ${BIN_SOURCE} (ejecuta antes 'make build')"
fi

# ── 1. Usuario/grupo de servicio (sin shell) ─────────────────────────────────
if ! getent group "${INSTALL_GROUP}" >/dev/null; then
    groupadd --system "${INSTALL_GROUP}"
    log "grupo ${INSTALL_GROUP} creado"
else
    log "grupo ${INSTALL_GROUP} ya existe"
fi
if ! getent passwd "${INSTALL_USER}" >/dev/null; then
    useradd --system --gid "${INSTALL_GROUP}" --shell /usr/sbin/nologin \
        --home-dir "${INSTALL_DIR}" "${INSTALL_USER}"
    log "usuario ${INSTALL_USER} creado (nologin, home ${INSTALL_DIR})"
else
    log "usuario ${INSTALL_USER} ya existe"
fi

# ── 2. Directorios y permisos ─────────────────────────────────────────────────
install -d -o "${INSTALL_USER}" -g "${INSTALL_GROUP}" \
    "${INSTALL_DIR}" "${INSTALL_DIR}/bin" "${INSTALL_DIR}/web" "${INSTALL_DIR}/data"
install -d -m 0700 -o root -g root "${SECRETS_DIR}"
log "directorios listos: ${INSTALL_DIR} (data/ dentro), ${SECRETS_DIR}"

# ── 3. secrets.env (placeholder; nunca sobrescribir; secretos fuera de Git) ──
if [[ ! -f "${SECRETS}" ]]; then
    cat > "${SECRETS}" <<EOF
# Abys-Invest: secrets del servicio ${SERVICE} (generado por deploy/setup.sh).
# RELLENA la DATABASE_URL real ANTES de habilitar el servicio. Este archivo no
# se versiona (secretos fuera de Git); chmod 600, owner abys:abys.
# Formato: postgres://usuario:password@host:puerto/bd?sslmode=disable
DATABASE_URL=postgres://CAMBIAR_USUARIO:CAMBIAR_PASSWORD@localhost:5432/CAMBIAR_BD?sslmode=disable
EOF
    chmod 0600 "${SECRETS}"
    chown "${INSTALL_USER}:${INSTALL_GROUP}" "${SECRETS}"
    log "secrets.env generado con PLACEHOLDER: ${SECRETS} (edítalo con la DATABASE_URL real)"
else
    log "secrets.env ya existe; no se sobrescribe: ${SECRETS}"
fi

# ── 3b. Guardas de --with-air (antes de instalar nada de air) ───────────────
# secrets_ready se define aquí porque la usan estas guardas y el guard de
# enable del paso 7 (una sola definición).
secrets_ready() {
    [[ -f "${SECRETS}" ]] || return 1
    local dsn
    dsn="$(grep -E '^DATABASE_URL=' "${SECRETS}" | tail -n 1 | cut -d= -f2- || true)"
    [[ -n "${dsn}" ]] || return 1
    case "${dsn}" in
        *CAMBIAR* | *CHANGEME* | *REEMPLAZAR* | *PLACEHOLDER*) return 1 ;;
    esac
    return 0
}

if [[ "${WITH_AIR}" == "1" ]]; then
    command -v go >/dev/null 2>&1 || die "--with-air requiere la toolchain Go en el servidor: no se encontró 'go' en el PATH. Instala Go o despliega sin --with-air (binario estático)."
    secrets_ready || die "--with-air requiere DATABASE_URL real en ${SECRETS} (placeholder o ausente). Edita ${SECRETS} con la URL real y vuelve a ejecutar."
    [[ -f "${AIR_CONFIG_SRC}" ]] || die "--with-air: no se encontró ${AIR_CONFIG_SRC} (deploy/.air.prod.toml)"
    log "guardas --with-air OK (toolchain go + secrets.env real)"
fi

# ── 4. Binario con preservación para rollback (bin/api.prev) ─────────────────
# En modo air el binario lo compila air en el arranque: no se instala estático.
if [[ "${WITH_AIR}" == "1" ]]; then
    if [[ -f "${BIN_DEST}" ]]; then
        mv -f "${BIN_DEST}" "${BIN_PREV}"
        log "binario previo preservado como ${BIN_PREV} (air lo reemplazará en el primer build)"
    fi
    log "modo --with-air: binario estático omitido (lo compila air a ${BIN_DEST})"
else
    if [[ -f "${BIN_DEST}" ]]; then
        mv -f "${BIN_DEST}" "${BIN_PREV}"
        log "binario anterior preservado como ${BIN_PREV} (rollback disponible)"
    fi
    install -m 0755 -o "${INSTALL_USER}" -g "${INSTALL_GROUP}" "${BIN_SOURCE}" "${BIN_DEST}"
    log "binario instalado: ${BIN_DEST}"
fi

# ── 4b. Modo air: fuentes Go + air + config .air.prod.toml ───────────────────
if [[ "${WITH_AIR}" == "1" ]]; then
    # Copia limpia e idempotente de las fuentes que necesita `go build ./cmd/api`.
    rm -rf "${INSTALL_DIR}/src"
    install -d -o "${INSTALL_USER}" -g "${INSTALL_GROUP}" "${INSTALL_DIR}/src"
    cp -a "${_REPO_ROOT}/cmd" "${_REPO_ROOT}/internal" \
        "${_REPO_ROOT}/go.mod" "${_REPO_ROOT}/go.sum" "${INSTALL_DIR}/src/"
    chown -R "${INSTALL_USER}:${INSTALL_GROUP}" "${INSTALL_DIR}/src"
    log "fuentes Go copiadas → ${INSTALL_DIR}/src (cmd/, internal/, go.mod, go.sum)"

    GOBIN="$(dirname "${AIR_BIN}")" go install github.com/air-verse/air@latest
    [[ -x "${AIR_BIN}" ]] || die "--with-air: air no quedó instalado en ${AIR_BIN} (revisa GOPROXY/GOPATH)"
    air_version="$("${AIR_BIN}" -v 2>/dev/null | tail -n1 || true)"
    log "air instalado: ${AIR_BIN} ${air_version}"

    install -m 0644 -o root -g root "${AIR_CONFIG_SRC}" "${AIR_CONFIG_DEST}"
    log "config air instalada: ${AIR_CONFIG_DEST}"
fi

# ── 5. Frontend (web/dist) ───────────────────────────────────────────────────
if [[ -d "${DIST_SOURCE}" ]]; then
    install -d -o "${INSTALL_USER}" -g "${INSTALL_GROUP}" "${INSTALL_DIR}/web/dist"
    cp -a "${DIST_SOURCE}/." "${INSTALL_DIR}/web/dist/"
    chown -R "${INSTALL_USER}:${INSTALL_GROUP}" "${INSTALL_DIR}/web/dist"
    log "frontend copiado: ${DIST_SOURCE} → ${INSTALL_DIR}/web/dist"
else
    log "AVISO: ${DIST_SOURCE} no existe; el API arrancará solo con rutas API (sin SPA)"
fi

# ── 6. Unit systemd + daemon-reload ──────────────────────────────────────────
install -m 0644 -o root -g root "${UNIT_SRC}" "/etc/systemd/system/${SERVICE}.service"
systemctl daemon-reload
log "unit instalada: /etc/systemd/system/${SERVICE}.service"

# ── 7. Guard de enable: solo si secrets.env tiene DATABASE_URL real ─────────
# (secrets_ready() está definida arriba, en las guardas de --with-air).
#
# Espera a que systemd reporte el servicio activo (hasta START_GRACE_S).
wait_active() {
    local i
    for ((i = 0; i < START_GRACE_S; i++)); do
        if systemctl is-active --quiet "${SERVICE}"; then
            return 0
        fi
        sleep 1
    done
    return 1
}

# Rollback explícito: restaura bin/api.prev y reinicia (solo binario; la unit
# y secrets.env no cambian).
rollback() {
    log "rollback: el servicio ${SERVICE} no quedó activo; restauro ${BIN_PREV}"
    systemctl stop "${SERVICE}" 2>/dev/null || true
    if [[ -f "${BIN_PREV}" ]]; then
        mv -f "${BIN_PREV}" "${BIN_DEST}"
        chown "${INSTALL_USER}:${INSTALL_GROUP}" "${BIN_DEST}"
        log "binario anterior restaurado en ${BIN_DEST}"
    else
        log "no existe ${BIN_PREV} (primera instalación); se conserva el binario nuevo para diagnóstico"
    fi
    systemctl restart "${SERVICE}" 2>/dev/null || true
    if wait_active; then
        log "servicio reactivado con el binario anterior"
    else
        log "AVISO: tampoco arranca con el binario anterior — revisar:"
        log "  systemctl status ${SERVICE}   (estado/failed)"
        log "  journalctl -u ${SERVICE} --no-pager -n 50   (logs del fallo)"
    fi
}

STARTED=0
if secrets_ready; then
    if systemctl is-enabled --quiet "${SERVICE}" 2>/dev/null; then
        systemctl restart "${SERVICE}"
        log "servicio reiniciado (re-deploy) para cargar el binario nuevo"
    else
        systemctl enable --now "${SERVICE}"
        log "servicio habilitado y arrancado (secrets.env rellenado)"
    fi
    STARTED=1
    if wait_active; then
        log "servicio activo (systemd) en ≤${START_GRACE_S}s"
    else
        log "AVISO: el servicio no quedó activo dentro de ${START_GRACE_S}s"
        rollback
    fi
else
    log "guard de enable: secrets.env sin DATABASE_URL real (placeholder o ausente)"
    log "  → la unit quedó instalada (daemon-reload hecho) pero el servicio NO se habilitó"
    log "  → edita ${SECRETS} y ejecuta: sudo systemctl enable --now ${SERVICE}"
fi

# ── 8. Health check final (con guardado: solo si se arrancó el servicio) ─────
check_health() {
    local body code
    body="$(curl -s --max-time "${HEALTH_TIMEOUT_S}" "${HEALTH_URL}" 2>/dev/null)" || true
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time "${HEALTH_TIMEOUT_S}" "${HEALTH_URL}" 2>/dev/null)" || code="000"
    if [[ "${code}" != "200" ]]; then
        printf 'HTTP %s\n' "${code}"
        return 1
    fi
    if command -v jq >/dev/null 2>&1; then
        if [[ "$(printf '%s' "${body}" | jq -r '.status // "unknown"')" != "ok" ]]; then
            printf 'HTTP 200 pero .status != "ok" (API degradada)\n'
            return 1
        fi
    fi
    printf 'HTTP 200 (.status=ok)\n'
    return 0
}

if [[ "${STARTED}" == "1" ]]; then
    if out="$(check_health)"; then
        log "health check OK: ${HEALTH_URL} → ${out}"
    else
        die "health check falló: ${HEALTH_URL} → ${out}. Sugerencias: revisa DATABASE_URL en ${SECRETS}, que Postgres responda (host/puerto), 'systemctl status ${SERVICE}' y 'journalctl -u ${SERVICE} --no-pager -n 50'."
    fi
else
    log "health check omitido (el servicio no fue arrancado: secrets.env pendiente de rellenar)"
fi

if [[ "${WITH_AIR}" == "1" ]]; then
    log "setup.sh completado (modo --with-air). Resumen: fuentes en ${INSTALL_DIR}/src, air en ${AIR_BIN}, config en ${AIR_CONFIG_DEST}, unit ${SERVICE}, frontend en ${INSTALL_DIR}/web/dist."
else
    log "setup.sh completado. Resumen: binario en ${BIN_DEST}, frontend en ${INSTALL_DIR}/web/dist, unit ${SERVICE}, rollback en ${BIN_PREV}."
fi