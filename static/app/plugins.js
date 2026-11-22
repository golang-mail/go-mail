// plugins: function declarations shared globally with the other app/*.js files.

async function fetchPluginNames() {
    if (pluginNamesCache !== null) return pluginNamesCache;
    try {
        const res = await fetch('/api/plugins', { credentials: 'include' });
        if (!res.ok) { pluginNamesCache = []; return pluginNamesCache; }
        const data = await res.json();
        pluginNamesCache = (data || [])
            .filter(p => !(p.status && p.status.running === false))
            .map(p => p.name);
    } catch (e) {
        pluginNamesCache = [];
    }
    return pluginNamesCache;
}

function openPlugins(evt) {
    setViewHash('plugins');
    openTab(evt, 'plugins-content', 'mail-tabcontent', 'mail-tablinks');
    loadPlugins();
    startPluginsRefresh();
    pluginNamesCache = null; // bust the autocomplete cache too — visible plugin set may change here
}

function startPluginsRefresh() {
    stopPluginsRefresh();
    pluginsRefreshTimer = setInterval(() => {
        const el = document.getElementById('plugins-content');
        const visible = el && el.style.display !== 'none';
        if (visible) {
            loadPlugins(true);
        } else {
            // Stop polling once the operator navigates away — no work to do.
            stopPluginsRefresh();
        }
    }, PLUGINS_REFRESH_MS);
}

function stopPluginsRefresh() {
    if (pluginsRefreshTimer) {
        clearInterval(pluginsRefreshTimer);
        pluginsRefreshTimer = null;
    }
}

async function loadPlugins(silent) {
    try {
        const res = await fetch('/api/plugins', { credentials: 'include' });
        checkResponse(res);
        if (!res.ok) throw new Error('failed to load plugins: ' + res.status);
        const plugins = await res.json() || [];
        pluginsData = plugins;
        renderPluginsList(plugins);
    } catch (err) {
        if (!silent) {
            document.getElementById('plugins-list').innerText = 'Plugins API not available: ' + err.message;
        }
    }
}

// renderPluginsList paints one row per configured plugin. Click anywhere
// outside the inline buttons to open the editor; inline buttons trigger
// their own action and stop propagation.
function renderPluginsList(plugins) {
    const container = document.getElementById('plugins-list');
    const count = document.getElementById('plugins-count');
    const total = plugins ? plugins.length : 0;
    if (count) count.textContent = `${total} plugin${total === 1 ? '' : 's'}`;
    if (!plugins || plugins.length === 0) {
        container.innerHTML = '<div class="filter-list-empty">No plugins configured. Click "Add Plugin" to register one.</div>';
        return;
    }
    const esc = escapeHTMLPlugins;
    container.innerHTML = plugins.map(p => {
        const name = p.name || '';
        const status = p.status || {};
        const running = status.running === true;
        const disabled = status.disabled === true;
        const transport = (p.spec && p.spec.transport) || 'socket';
        const restarts = status.restarts || 0;
        const lastErr = status.last_error || '';
        const usageCount = (p.usage && p.usage.filter_count) || 0;
        const version = (p.manifest && p.manifest.version) || '';
        const wantsSchedule = !!(p.manifest && p.manifest.wants_schedule);

        let badge;
        if (running) badge = '<span class="plugin-status plugin-status-running">running</span>';
        else if (disabled) badge = '<span class="plugin-status plugin-status-disabled">disabled</span>';
        else badge = '<span class="plugin-status plugin-status-stopped">stopped</span>';

        const parts = [
            `<span class="filter-card-count">${esc(transport)}</span>`,
            `<span>${usageCount > 0 ? usageCount + ' filter' + (usageCount !== 1 ? 's' : '') : 'no filters'}</span>`,
        ];
        if (restarts > 0) parts.push(`<span>${restarts} restart${restarts !== 1 ? 's' : ''}</span>`);
        const err = lastErr ? `<div class="plugin-card-error" title="${esc(lastErr)}">${esc(lastErr)}</div>` : '';

        let tools = '';
        if (wantsSchedule && running) {
            tools += `<button class="btn btn-default btn-sm-outline" data-action="plugin-trigger" data-name="${esc(name)}" title="Fire scheduled_tick now instead of waiting for the next cron tick">Run Now</button>`;
        }
        if (!disabled) {
            tools += `<button class="btn btn-default btn-sm-outline" data-action="plugin-restart" data-name="${esc(name)}">Restart</button>`;
        }
        tools += `<button class="btn btn-default btn-sm-outline" data-action="plugin-toggle" data-name="${esc(name)}" data-disabled="${disabled ? '1' : ''}">${disabled ? 'Enable' : 'Disable'}</button>`;

        return `<div class="filter-card" data-name="${esc(name)}">
            <div class="filter-card-head">
                <div class="filter-card-name">${esc(name)}${version ? ` <span class="plugin-version">v${esc(version)}</span>` : ''}</div>
                <div class="filter-card-scope">${badge}</div>
                <div class="filter-card-tools">${tools}</div>
            </div>
            <div class="filter-card-summary">${parts.join('<span class="filter-card-joiner">&middot;</span>')}</div>
            ${err}
        </div>`;
    }).join('');

    container.querySelectorAll('.filter-card').forEach(card => {
        card.addEventListener('click', (e) => {
            if (e.target.closest('[data-action]')) return;
            openPluginEditor(card.dataset.name);
        });
    });
}

// restartPluginAction is invoked from the inline "Restart" button. Skips
// the editor entirely so the operator can nudge a stuck plugin in one click.
async function restartPluginAction(evt, name) {
    evt.stopPropagation();
    try {
        const res = await fetch('/api/plugins/' + encodeURIComponent(name) + '/restart', { method: 'POST', credentials: 'include' });
        if (!res.ok) throw new Error('status ' + res.status);
        showToast('Plugin "' + name + '" restarted', 'success');
        loadPlugins();
    } catch (err) {
        showToast('Restart failed: ' + err.message, 'error');
    }
}

// togglePluginAction flips the operator disabled flag from the card. The
// button carries the state it was rendered with so a stale card cannot
// re-disable a plugin the operator just enabled elsewhere.
async function togglePluginAction(evt, name, currentlyDisabled) {
    evt.stopPropagation();
    const action = currentlyDisabled ? 'enable' : 'disable';
    try {
        const res = await fetch('/api/plugins/' + encodeURIComponent(name) + '/' + action, { method: 'POST', credentials: 'include' });
        if (!res.ok) throw new Error('status ' + res.status);
        showToast('Plugin "' + name + '" ' + action + 'd', 'success');
        loadPlugins();
    } catch (err) {
        showToast('Failed to ' + action + ' plugin: ' + err.message, 'error');
    }
}

// triggerPluginAction fires a scheduled_tick at a scheduled plugin so the
// operator doesn't have to wait for the next natural cron fire.
async function triggerPluginAction(evt, name) {
    evt.stopPropagation();
    try {
        const res = await fetch('/api/plugins/' + encodeURIComponent(name) + '/trigger', { method: 'POST', credentials: 'include' });
        if (!res.ok) {
            const text = await res.text();
            throw new Error(text || ('status ' + res.status));
        }
        showToast('Plugin "' + name + '" triggered', 'success');
    } catch (err) {
        showToast('Trigger failed: ' + err.message, 'error');
    }
}

async function openPluginEditor(name) {
    try {
        const res = await fetch('/api/plugins/' + encodeURIComponent(name), { credentials: 'include' });
        if (!res.ok) throw new Error('failed to load plugin: ' + res.status);
        const detail = await res.json();
        populatePluginEditor(detail, 'edit');
        openTab(null, 'plugin-editor-content', 'mail-tabcontent', 'mail-tablinks');
    } catch (err) {
        showToast('Open editor failed: ' + err.message, 'error');
    }
}

function openNewPluginEditor() {
    populatePluginEditor(null, 'create');
    openTab(null, 'plugin-editor-content', 'mail-tabcontent', 'mail-tablinks');
}

// populatePluginEditor wires a detail payload (or null for create mode)
// into the editor form. Buttons are toggled by mode so create-mode only
// shows Save/Cancel and edit-mode shows the full action set.
function populatePluginEditor(detail, mode) {
    pluginEditorMode = mode;
    pluginEditorName = (mode === 'edit' && detail) ? detail.name : null;
    pluginEditorSecretPaths = (detail && detail.config_secret_paths) || [];

    document.getElementById('plugin-editor-title').textContent =
        mode === 'edit' ? ('Edit Plugin: ' + (detail.name || '')) : 'Add Plugin';

    const spec = (detail && detail.spec) || {};
    document.getElementById('plugin-name-input').value = (detail && detail.name) || '';
    // Name is the identity used by filters — disable in edit mode so a
    // rename can't silently orphan filter wiring.
    document.getElementById('plugin-name-input').disabled = (mode === 'edit');
    document.getElementById('plugin-command-input').value = (spec.command || []).join(' ');
    document.getElementById('plugin-transport-input').value = spec.transport || 'socket';
    document.getElementById('plugin-timeout-input').value = formatGoDuration(spec.timeout);
    document.getElementById('plugin-disabled-input').checked = !!spec.disabled;

    // Render the config as pretty JSON so the textarea is JSON-only.
    // On disk plugins.yml stays YAML — the server round-trips between
    // the two. JSON-only in-browser keeps the parse path simple and
    // avoids shipping a YAML parser to every page load.
    let configText = '';
    if (spec.config !== undefined && spec.config !== null) {
        try {
            configText = JSON.stringify(spec.config, null, 2);
        } catch (e) {
            configText = String(spec.config);
        }
    }
    document.getElementById('plugin-config-input').value = configText;

    const statusBlock = document.getElementById('plugin-editor-status');
    const statusBody = document.getElementById('plugin-editor-status-body');
    if (mode === 'edit' && detail && detail.status) {
        statusBlock.style.display = '';
        const s = detail.status;
        const running = s.running ? '<span style="color:#137333;">● running</span>' : '<span style="color:#c5221f;">⊘ stopped</span>';
        const startedAt = s.last_started ? new Date(s.last_started).toLocaleString() : 'never';
        let body = running + ' &nbsp; restarts: ' + (s.restarts || 0) + ' &nbsp; last_started: ' + escapeHTMLPlugins(startedAt);
        if (s.last_error) {
            body += '<br><span style="color:#c5221f;">' + escapeHTMLPlugins(s.last_error) + '</span>';
        }
        statusBody.innerHTML = body;
    } else {
        statusBlock.style.display = 'none';
    }

    const manifestBlock = document.getElementById('plugin-editor-manifest');
    const manifestBody = document.getElementById('plugin-editor-manifest-body');
    if (mode === 'edit' && detail && detail.manifest) {
        manifestBlock.style.display = '';
        const m = detail.manifest;
        const rows = [
            ['name', m.name],
            ['version', m.version],
            ['actions', (m.actions || []).join(', ')],
            ['wants', (m.wants || []).join(', ')],
            ['cacheable', m.cacheable ? 'true' : 'false'],
            ['run_last', m.run_last === false ? 'false' : 'true'],
            ['rerunnable', m.rerunnable ? 'true' : 'false'],
            ['rescan_interval', m.rescan_interval || ''],
            ['wants_batch_complete', m.wants_batch_complete ? 'true' : 'false'],
            ['wants_mailbox_state', m.wants_mailbox_state ? 'true' : 'false'],
            ['wants_schedule', m.wants_schedule ? 'true' : 'false'],
            ['schedule', m.schedule || ''],
            ['host_methods', (m.host_methods || []).join(', ')],
        ];
        let mh = '<table style="font-size:12px; width:100%;">';
        rows.forEach(r => {
            mh += '<tr><td style="padding:2px 8px 2px 0; color:var(--secondary-color); width:160px;">' + r[0] + '</td><td>' + escapeHTMLPlugins(String(r[1] || '—')) + '</td></tr>';
        });
        mh += '</table>';
        manifestBody.innerHTML = mh;
    } else {
        manifestBlock.style.display = 'none';
    }

    const usageBlock = document.getElementById('plugin-editor-usage');
    const usageBody = document.getElementById('plugin-editor-usage-body');
    if (mode === 'edit' && detail && detail.usage && detail.usage.filter_count > 0) {
        usageBlock.style.display = '';
        const filters = (detail.usage.filters || []).map(f => escapeHTMLPlugins(f)).join(', ');
        usageBody.innerHTML = Number(detail.usage.filter_count) + ' filter' + (detail.usage.filter_count !== 1 ? 's' : '') + ': ' + filters;
    } else {
        usageBlock.style.display = 'none';
    }

    document.getElementById('plugin-config-mask-hint').style.display =
        (pluginEditorSecretPaths.length > 0) ? '' : 'none';

    const inEdit = (mode === 'edit');
    document.getElementById('save-restart-plugin').style.display = inEdit ? '' : 'none';
    document.getElementById('restart-plugin').style.display = inEdit ? '' : 'none';
    const wantsSchedule = !!(detail && detail.manifest && detail.manifest.wants_schedule);
    const triggerBtn = document.getElementById('trigger-plugin');
    triggerBtn.style.display = (inEdit && wantsSchedule) ? '' : 'none';
    document.getElementById('toggle-plugin-disabled').style.display = inEdit ? '' : 'none';
    document.getElementById('delete-plugin').style.display = inEdit ? '' : 'none';
    if (inEdit) {
        document.getElementById('toggle-plugin-disabled').textContent = spec.disabled ? 'Enable' : 'Disable';
    }
}

// escapeHTMLPlugins — a tiny standalone HTML escaper so the plugins UI
// does not depend on whatever escapeHTML the page may or may not have
// exposed (the codebase mixes DOMPurify, ad-hoc replace chains, and
// direct innerHTML). Self-contained = no spooky action at a distance.
function escapeHTMLPlugins(s) {
    return String(s == null ? '' : s)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;');
}

async function savePlugin(restartAfter) {
    const name = document.getElementById('plugin-name-input').value.trim();
    if (!name) { showToast('Name is required', 'error'); return; }
    const commandStr = document.getElementById('plugin-command-input').value.trim();
    if (!commandStr) { showToast('Command is required', 'error'); return; }

    const configText = document.getElementById('plugin-config-input').value;
    let configObj = null;
    if (configText.trim() !== '') {
        try {
            configObj = JSON.parse(configText);
        } catch (err) {
            showToast('Config must be valid JSON: ' + err.message, 'error');
            return;
        }
    }

    let timeoutNs;
    try {
        timeoutNs = parseDurationToNs(document.getElementById('plugin-timeout-input').value);
    } catch (err) {
        showToast(err.message, 'error');
        return;
    }

    const spec = {
        name: name,
        command: commandStr.split(/\s+/),
        transport: document.getElementById('plugin-transport-input').value,
        disabled: document.getElementById('plugin-disabled-input').checked,
        config: configObj,
    };
    if (timeoutNs > 0) spec.timeout = timeoutNs;

    try {
        let res;
        if (pluginEditorMode === 'create') {
            res = await fetch('/api/plugins', {
                method: 'POST',
                credentials: 'include',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify(spec),
            });
        } else {
            res = await fetch('/api/plugins/' + encodeURIComponent(pluginEditorName), {
                method: 'PUT',
                credentials: 'include',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify(spec),
            });
        }
        if (!res.ok) {
            const txt = await res.text();
            throw new Error(txt.trim() || ('status ' + res.status));
        }
        if (restartAfter && pluginEditorMode === 'edit') {
            const rres = await fetch('/api/plugins/' + encodeURIComponent(name) + '/restart', { method: 'POST', credentials: 'include' });
            if (!rres.ok) { showToast('Saved, but restart failed: ' + rres.status, 'error'); }
        }
        showToast('Plugin saved', 'success');
        openTab(null, 'plugins-content', 'mail-tabcontent', 'mail-tablinks');
        loadPlugins();
    } catch (err) {
        showToast('Save failed: ' + err.message, 'error');
    }
}

async function deletePluginAction() {
    if (!pluginEditorName) return;
    if (!(await showConfirm('Delete plugin "' + pluginEditorName + '"? Filters referencing it will silently no-op.', 'Delete'))) return;
    try {
        const res = await fetch('/api/plugins/' + encodeURIComponent(pluginEditorName), { method: 'DELETE', credentials: 'include' });
        if (!res.ok && res.status !== 204) throw new Error('status ' + res.status);
        showToast('Plugin deleted', 'success');
        openTab(null, 'plugins-content', 'mail-tabcontent', 'mail-tablinks');
        loadPlugins();
    } catch (err) {
        showToast('Delete failed: ' + err.message, 'error');
    }
}

async function togglePluginDisabledAction() {
    if (!pluginEditorName) return;
    // The button label reflects what'll happen next (Enable when currently
    // disabled, Disable when currently enabled). Read it instead of the
    // checkbox so the action stays consistent if the operator toggled the
    // checkbox manually without saving.
    const willDisable = document.getElementById('toggle-plugin-disabled').textContent.trim() === 'Disable';
    const action = willDisable ? 'disable' : 'enable';
    try {
        const res = await fetch('/api/plugins/' + encodeURIComponent(pluginEditorName) + '/' + action, { method: 'POST', credentials: 'include' });
        if (!res.ok) throw new Error('status ' + res.status);
        showToast('Plugin ' + (willDisable ? 'disabled' : 'enabled'), 'success');
        openPluginEditor(pluginEditorName);
        loadPlugins();
    } catch (err) {
        showToast(action + ' failed: ' + err.message, 'error');
    }
}

async function restartPluginFromEditor() {
    if (!pluginEditorName) return;
    try {
        const res = await fetch('/api/plugins/' + encodeURIComponent(pluginEditorName) + '/restart', { method: 'POST', credentials: 'include' });
        if (!res.ok) throw new Error('status ' + res.status);
        showToast('Plugin restarted', 'success');
        openPluginEditor(pluginEditorName);
    } catch (err) {
        showToast('Restart failed: ' + err.message, 'error');
    }
}

async function triggerPluginFromEditor() {
    if (!pluginEditorName) return;
    try {
        const res = await fetch('/api/plugins/' + encodeURIComponent(pluginEditorName) + '/trigger', { method: 'POST', credentials: 'include' });
        if (!res.ok) {
            const text = await res.text();
            throw new Error(text || ('status ' + res.status));
        }
        showToast('Plugin triggered', 'success');
    } catch (err) {
        showToast('Trigger failed: ' + err.message, 'error');
    }
}
