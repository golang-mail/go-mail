// filters: function declarations shared globally with the other app/*.js files.

async function loadFilters() {
    try {
        const res = await fetch('/api/filters', { credentials: 'include' });
        checkResponse(res);
        if (!res.ok) throw new Error('no filters endpoint');
        allFilters = await res.json();
        renderFilteredList();
    } catch (err) {
        document.getElementById('filters-list').innerText = 'Filters API not available or failed: ' + err.message;
    }
}

function filterMatchesQuery(f, q) {
    if (!q) return true;
    const filterObj = f.filter || {};
    const parts = [
        f.name || '',
        (filterObj.Accounts || []).join(' '),
        (filterObj.Mailboxes || []).join(' '),
        (filterObj.Conditions || []).map(c => `${c.Field || ''} ${c.Operator || ''} ${c.Value || ''}`).join(' '),
        (filterObj.Actions || []).map(a => `${a.Type || ''} ${a.Value || ''}`).join(' '),
    ].join(' ').toLowerCase();
    return parts.includes(q);
}

function renderFilteredList() {
    const q = (document.getElementById('filter-search')?.value || '').trim().toLowerCase();
    const matched = allFilters.filter(f => filterMatchesQuery(f, q));
    renderFiltersList(matched);
}

const FILTER_OPERATOR_WORDS = {
    'contains': 'contains', 'equals': 'equals', 'not-contains': 'does not contain',
    'begins_with': 'begins with', 'ends_with': 'ends with', 'matches': 'matches',
    'older_than': 'older than', 'exists': 'exists',
};

function describeCondition(c) {
    const field = c.Field || c.field || '';
    const op = c.Operator || c.operator || '';
    const value = c.Value || c.value || '';
    const opWord = FILTER_OPERATOR_WORDS[op] || op;
    if (op === 'exists' || field === 'status') return `${field} ${op === 'exists' ? 'exists' : 'is ' + value}`.trim();
    return `${field} ${opWord} ${value}`.trim();
}

function describeAction(a) {
    const type = a.Type || a.type || '';
    const value = a.Value || a.value || '';
    switch (type) {
        case 'move': return 'move to ' + value;
        case 'mark_as_read': return 'mark as read';
        case 'mark_as_unread': return 'mark as unread';
        case 'flag': return 'flag ' + value;
        case 'label': return 'label ' + value;
        case 'delete': return 'delete';
        case 'permanent_delete': return 'permanently delete';
        case 'mark_as_spam': return 'mark as spam';
        case 'plugin_clear_cache': return 'clear plugin cache ' + value;
        default: return (type + ' ' + value).trim();
    }
}

// Normalizes both the current {filter: {...}} shape and the legacy flat fields.
function filterListEntry(f) {
    const base = f.filter || {};
    const conditions = (base.Conditions && base.Conditions.length) ? base.Conditions
        : (f.field ? [{ Field: f.field, Operator: f.op, Value: f.value }] : []);
    const actions = (base.Actions && base.Actions.length) ? base.Actions
        : (f.action ? [{ Type: f.action, Value: f.action_param }] : []);
    return {
        id: f.id,
        name: f.name || base.Name || '',
        accounts: (base.Accounts && base.Accounts.length) ? base.Accounts : (f.account ? [f.account] : []),
        mailboxes: (base.Mailboxes && base.Mailboxes.length) ? base.Mailboxes : (f.mailbox ? [f.mailbox] : []),
        any: (base.ConditionsType || 'and') === 'or',
        disabled: !!base.disabled,
        activeFrom: base.active_from || '',
        activeUntil: base.active_until || '',
        conditions,
        actions,
    };
}

function formatScheduleTime(iso) {
    const d = new Date(iso);
    return isNaN(d) ? iso : d.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' });
}

// Status chip text: "disabled", "starts <when>", "until <when>", or '' when plain.
function describeSchedule(entry) {
    const parts = [];
    if (entry.disabled) parts.push('disabled');
    if (entry.activeFrom) parts.push('starts ' + formatScheduleTime(entry.activeFrom));
    if (entry.activeUntil) parts.push('until ' + formatScheduleTime(entry.activeUntil));
    return parts.join(' \u00b7 ');
}

// datetime-local works in local wall time; the API wants RFC 3339 in UTC.
function scheduleInputToISO(value) {
    if (!value) return '';
    const d = new Date(value);
    return isNaN(d) ? '' : d.toISOString().replace(/\.\d{3}Z$/, 'Z');
}

function scheduleISOToInput(iso) {
    if (!iso) return '';
    const d = new Date(iso);
    if (isNaN(d)) return '';
    const pad = n => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function updateOnExpireVisibility() {
    document.getElementById('filter-on-expire-row').hidden = !document.getElementById('filter-active-until').value;
}

function clearFilterInput(id) {
    document.getElementById(id).value = '';
    updateOnExpireVisibility();
}

function setFilterSchedule(f) {
    document.getElementById('filter-disabled').checked = !!(f && f.disabled);
    document.getElementById('filter-active-from').value = scheduleISOToInput(f && f.active_from);
    document.getElementById('filter-active-until').value = scheduleISOToInput(f && f.active_until);
    document.getElementById('filter-on-expire').value = (f && f.on_expire) || 'disable';
    updateOnExpireVisibility();
}

async function toggleFilterEnabled(event, id) {
    if (event && event.stopPropagation) event.stopPropagation();
    try {
        const res = await fetch('/api/filters/' + encodeURIComponent(id), { credentials: 'include' });
        if (!res.ok) throw new Error('failed to load');
        const f = await res.json();
        const base = f.filter || f;
        base.disabled = !base.disabled;
        const put = await fetch('/api/filters/' + encodeURIComponent(id), { method: 'PUT', credentials: 'include', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(base) });
        if (!put.ok) throw new Error('save failed: ' + put.status);
        showToast(base.disabled ? 'Filter disabled' : 'Filter enabled', 'success');
        loadFilters();
    } catch (err) {
        showToast('Failed to update filter: ' + err.message, 'error');
    }
}

function renderFilterCard(entry) {
    const scope = [];
    if (entry.accounts.length) {
        entry.accounts.forEach(a => scope.push(`<span class="filter-chip">${escapeHtml(a)}</span>`));
    } else {
        scope.push('<span class="filter-chip filter-chip-muted">all accounts</span>');
    }
    const mailboxes = entry.mailboxes.length ? entry.mailboxes : ['INBOX'];
    mailboxes.forEach(m => scope.push(`<span class="filter-chip filter-chip-mailbox">${escapeHtml(m)}</span>`));
    const schedule = describeSchedule(entry);
    if (schedule) scope.push(`<span class="filter-chip filter-chip-status">${escapeHtml(schedule)}</span>`);
    const toggleTitle = entry.disabled ? 'Enable filter' : 'Disable filter';
    const toggleIcon = entry.disabled
        ? '<path d="M8 5v14l11-7z"/>'
        : '<path d="M6 19h4V5H6v14zm8-14v14h4V5h-4z"/>';

    const joiner = entry.any ? ' or ' : ' and ';
    let conds;
    if (entry.conditions.length === 0) {
        conds = '<span class="filter-card-muted">no conditions</span>';
    } else if (entry.conditions.length <= 2) {
        conds = entry.conditions.map(c => `<span class="filter-card-cond">${escapeHtml(describeCondition(c))}</span>`).join(`<span class="filter-card-joiner">${joiner}</span>`);
    } else {
        conds = `<span class="filter-card-count">${entry.any ? 'Any' : 'All'} of ${entry.conditions.length} conditions</span>`;
    }
    const acts = entry.actions.length
        ? entry.actions.map(a => `<span class="filter-card-act">${escapeHtml(describeAction(a))}</span>`).join('<span class="filter-card-joiner">, </span>')
        : '<span class="filter-card-muted">no actions</span>';

    const detailConds = entry.conditions.map(c => `<li class="filter-card-cond">${escapeHtml(describeCondition(c))}</li>`).join('');
    const detailActs = entry.actions.map(a => `<li class="filter-card-act">${escapeHtml(describeAction(a))}</li>`).join('');
    const matchWord = entry.conditions.length > 1 ? (entry.any ? 'Any of' : 'All of') : '';

    return `<div class="filter-card${entry.disabled ? ' is-disabled' : ''}" data-id="${escapeAttr(entry.id)}">
        <div class="filter-card-head">
            <div class="filter-card-name">${escapeHtml(entry.name) || '<span class="filter-card-muted">unnamed</span>'}</div>
            <div class="filter-card-scope">${scope.join('')}</div>
            <div class="filter-card-tools">
                <button class="btn-icon filter-card-enable" data-id="${escapeAttr(entry.id)}" data-action="toggle-filter-enabled" title="${toggleTitle}" aria-label="${toggleTitle}" aria-pressed="${entry.disabled ? 'true' : 'false'}">
                    <svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor">${toggleIcon}</svg>
                </button>
                <button class="btn-icon filter-card-toggle" data-action="toggle-filter-card" title="Show conditions" aria-label="Show conditions" aria-expanded="false">
                    <svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor"><path d="M7.41 8.59 12 13.17l4.59-4.58L18 10l-6 6-6-6z"/></svg>
                </button>
                <button class="btn-icon filter-card-delete" data-id="${escapeAttr(entry.id)}" data-action="delete-filter" title="Delete filter" aria-label="Delete filter">
                    ${svgDeleteIcon(18)}
                </button>
            </div>
        </div>
        <div class="filter-card-summary">${conds}<span class="filter-card-arrow">&rarr;</span>${acts}</div>
        <div class="filter-card-details" hidden>
            <div class="filter-card-details-title">${matchWord ? matchWord + ' ' : ''}${entry.conditions.length} condition${entry.conditions.length === 1 ? '' : 's'}</div>
            <ul>${detailConds || '<li class="filter-card-muted">none</li>'}</ul>
            <div class="filter-card-details-title">Actions</div>
            <ul>${detailActs || '<li class="filter-card-muted">none</li>'}</ul>
        </div>
    </div>`;
}

function renderFiltersList(filters) {
    const container = document.getElementById('filters-list');
    const count = document.getElementById('filters-count');
    if (count) {
        const total = allFilters.length;
        const shown = filters ? filters.length : 0;
        count.textContent = shown === total ? `${total} filter${total === 1 ? '' : 's'}` : `${shown} of ${total} filters`;
    }
    if (!filters || filters.length === 0) {
        container.innerHTML = '<div class="filter-list-empty">No filters defined</div>';
        return;
    }
    container.innerHTML = filters.map(f => renderFilterCard(filterListEntry(f))).join('');

    container.querySelectorAll('.filter-card').forEach(card => {
        card.addEventListener('click', (e) => {
            if (e.target.closest('[data-action]')) return;
            openFilterEditor(card.dataset.id);
        });
    });
}

function toggleFilterCard(evt) {
    const btn = evt.target.closest('.filter-card-toggle');
    const card = btn && btn.closest('.filter-card');
    if (!card) return;
    const details = card.querySelector('.filter-card-details');
    const open = details.hidden;
    details.hidden = !open;
    card.classList.toggle('expanded', open);
    btn.setAttribute('aria-expanded', open ? 'true' : 'false');
    btn.title = open ? 'Hide conditions' : 'Show conditions';
    btn.setAttribute('aria-label', btn.title);
}

function deleteFilterWrapper(event, id) {
    event.stopPropagation();
    deleteFilter(id);
}

function openFilters(evt) {
    setViewHash('filters');
    openTab(evt, 'filters-content', 'mail-tabcontent', 'mail-tablinks');
    loadFilters();
}

async function deleteFilter(id) {
    if (!(await showConfirm('Delete filter?', 'Delete'))) return;
    try {
        const res = await fetch('/api/filters/' + encodeURIComponent(id), { method: 'DELETE', credentials: 'include' });
        checkResponse(res);
        if (!res.ok) throw new Error('delete failed');
        loadFilters();
    } catch (err) {
        showToast('Delete failed: ' + err.message, 'error');
    }
}

async function populateFilterEditorDropdowns() {
    const accounts = await fetchAccounts();
    const accountSelect = document.getElementById('filter-account-input');
    accountSelect.innerHTML = '';
    accounts.forEach(acc => {
        accountSelect.appendChild(new Option(acc.Name, acc.Name));
    });
    // Also populate label account dropdown
    const labelAccountSelect = document.getElementById('label-account-input');
    if (labelAccountSelect) {
        labelAccountSelect.innerHTML = '';
        accounts.forEach(acc => {
            labelAccountSelect.appendChild(new Option(acc.Name, acc.Name));
        });
    }

    accountSelect.addEventListener('change', async () => {
        const selectedAccount = accountSelect.value;
        let mailboxData = await fetchMailboxes(selectedAccount);
        let mailboxes = mailboxData.mailboxes;
        const mailboxSelect = document.getElementById('filter-mailbox-input');
        mailboxSelect.innerHTML = '';

        const inboxIndex = mailboxes.findIndex(m => m.toUpperCase() === 'INBOX');
        if (inboxIndex > -1) {
            const inbox = mailboxes.splice(inboxIndex, 1)[0];
            mailboxes.sort((a, b) => a.localeCompare(b));
            mailboxes.unshift(inbox);
        } else {
            mailboxes.sort((a, b) => a.localeCompare(b));
        }

        mailboxes.forEach(mbx => {
            mailboxSelect.appendChild(new Option(mbx, mbx));
        });
    });
    if (accounts.length > 0) {
        accountSelect.dispatchEvent(new Event('change'));
    }
}

async function openFilterEditor(id) {
    try {
        const res = await fetch('/api/filters/' + encodeURIComponent(id), { credentials: 'include' });
        if (!res.ok) throw new Error('failed to load');
        const f = await res.json();
        const base = f.filter || f;

        editorState.accounts = base.Accounts && base.Accounts.length ? base.Accounts.slice() : (f.account ? [f.account] : []);
        editorState.mailboxes = base.Mailboxes && base.Mailboxes.length ? base.Mailboxes.slice() : (f.mailbox ? [f.mailbox] : []);
        renderTagList('filter-accounts-list', editorState.accounts);
        renderTagList('filter-mailboxes-list', editorState.mailboxes);

        document.getElementById('filter-name').value = base.Name || f.name || '';
        document.getElementById('filter-conditions-type').value = base.ConditionsType || base.conditions_type || 'and';
        setFilterSchedule(base);

        const condContainer = document.getElementById('filter-conditions');
        const actContainer = document.getElementById('filter-actions');
        condContainer.innerHTML = '';
        actContainer.innerHTML = '';

        const conds = base.Conditions || base.conditions || [];
        conds.forEach(c => {
            const row = makeConditionRow({field: c.Field || c.field || '', operator: c.Operator || c.operator || '', value: c.Value || c.value || ''}, () => {});
            condContainer.appendChild(row);
        });

        const acts = base.Actions || base.actions || [];
        acts.forEach(a => {
            const row = makeActionRow({type: a.Type || a.type || '', value: a.Value || a.value || ''}, () => {});
            actContainer.appendChild(row);
        });

        document.getElementById('save-filter').dataset.editId = id;
        document.getElementById('new-filter-title').innerText = 'Edit Filter';
        openTab(null, 'new-filter-content', 'mail-tabcontent', 'mail-tablinks');
    } catch (err) {
        showToast('Failed to load filter for edit: ' + err.message, 'error');
    }
}

async function saveFilter() {
    const id = document.getElementById('save-filter').dataset.editId;
    const name = document.getElementById('filter-name').value || '';
    const accounts = editorState.accounts.slice();
    const mailboxes = editorState.mailboxes.slice();
    const conditionsType = document.getElementById('filter-conditions-type').value || 'and';

    const condContainer = document.getElementById('filter-conditions');
    const condNodes = Array.from(condContainer.children);
    const conditions = condNodes.map(node => {
        let fieldVal = node._field ? node._field.value : node.querySelector('select').value;
        const opVal = node._op ? node._op.value : node.querySelectorAll('select')[1].value;
        let valVal = node._val ? node._val.value : node.querySelector('input[type="text"]').value;
        if (fieldVal === 'header' && node._headerInput && node._headerInput.value) {
            fieldVal = 'header:' + node._headerInput.value;
        } else if (fieldVal === 'plugin' && node._pluginSel && node._pluginSel.value) {
            fieldVal = 'plugin:' + node._pluginSel.value;
        } else if (fieldVal === 'label' && node._labelSel) {
            valVal = node._labelSel.value;
        }
        return { field: fieldVal, operator: opVal, value: valVal };
    });

    const actContainer = document.getElementById('filter-actions');
    const actNodes = Array.from(actContainer.children);
    const actions = actNodes.map(node => {
        const type = node._actSel ? node._actSel.value : node.querySelector('select').value;
        let value = '';
        if (type === 'move' && node._mailboxSel) {
            value = node._mailboxSel.value;
        } else if (type === 'flag' && node._param) {
            value = node._param.value;
        } else if (type === 'label' && node._labelSel) {
            value = node._labelSel.value;
        } else if (type === 'plugin_clear_cache' && node._pluginActionSel) {
            value = node._pluginActionSel.value;
        }
        return { type, value };
    });

    const payload = {
        name: name,
        accounts: accounts,
        mailboxes: mailboxes,
        conditions_type: conditionsType,
        conditions: conditions,
        actions: actions,
        disabled: document.getElementById('filter-disabled').checked,
        active_from: scheduleInputToISO(document.getElementById('filter-active-from').value),
        active_until: scheduleInputToISO(document.getElementById('filter-active-until').value),
        on_expire: document.getElementById('filter-active-until').value ? document.getElementById('filter-on-expire').value : '',
    };

    try {
        let res;
        if (id) {
            res = await fetch('/api/filters/' + encodeURIComponent(id), { method: 'PUT', credentials: 'include', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
        } else {
            res = await fetch('/api/filters', { method: 'POST', credentials: 'include', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
        }
        if (!res.ok) throw new Error('save failed: ' + res.status);
        showToast('Saved', 'success');
        delete document.getElementById('save-filter').dataset.editId;
        document.getElementById('new-filter-title').innerText = 'New Filter';
        openTab(null, 'filters-content', 'mail-tabcontent', 'mail-tablinks');
        loadFilters();
    } catch (err) {
        showToast('Save failed: ' + err.message, 'error');
    }
}

async function showFilterPicker(headerName, headerValue, accountName) {
    try {
        const res = await fetch('/api/filters', { credentials: 'include' });
        if (!res.ok) throw new Error('failed to load filters');
        const filters = await res.json();
        if (!filters || filters.length === 0) {
            showToast('No existing filters. Use the filter icon to create a new one.', 'info');
            return;
        }
        const list = document.getElementById('filter-picker-list');
        list.innerHTML = '';
        filters.forEach(f => {
            const li = document.createElement('li');
            li.textContent = f.name || f.id;
            li.onclick = () => {
                document.getElementById('filter-picker-overlay').style.display = 'none';
                document.getElementById('filter-picker-modal').style.display = 'none';
                document.getElementById('headers-overlay').style.display = 'none';
                document.getElementById('headers-modal').style.display = 'none';
                openFilterEditorWithCondition(f.id, headerName, headerValue, accountName);
            };
            list.appendChild(li);
        });
        document.getElementById('filter-picker-overlay').style.display = 'block';
        document.getElementById('filter-picker-modal').style.display = 'block';
    } catch (err) {
        showToast('Failed to load filters: ' + err.message, 'error');
    }
}

async function openFilterEditorWithCondition(id, headerName, headerValue, accountName) {
    await openFilterEditor(id);
    const condContainer = document.getElementById('filter-conditions');
    const knownFields = ['from', 'sender', 'to', 'cc', 'bcc', 'from/to', 'from/to/cc/bcc', 'subject', 'body', 'age', 'status'];
    let lower = headerName.toLowerCase();
    if (lower.endsWith(':e-mail')) lower = lower.slice(0, -7);
    const field = knownFields.includes(lower) ? lower : 'header:' + headerName;
    const row = makeConditionRow({field: field, operator: 'equals', value: headerValue}, () => {});
    condContainer.appendChild(row);
}

function openNewFilter() {
    delete document.getElementById('save-filter').dataset.editId;
    document.getElementById('new-filter-title').innerText = 'New Filter';
    document.getElementById('filter-name').value = '';
    editorState.accounts = [];
    editorState.mailboxes = [];
    renderTagList('filter-accounts-list', editorState.accounts);
    renderTagList('filter-mailboxes-list', editorState.mailboxes);
    document.getElementById('filter-conditions-type').value = 'and';
    setFilterSchedule(null);
    const condContainer = document.getElementById('filter-conditions');
    condContainer.innerHTML = '';
    condContainer.appendChild(makeConditionRow({field: 'from', operator: 'contains', value: ''}, () => {}));
    const actContainer = document.getElementById('filter-actions');
    actContainer.innerHTML = '';
    actContainer.appendChild(makeActionRow({type: 'move', value: ''}, () => {}));
    openTab(null, 'new-filter-content', 'mail-tabcontent', 'mail-tablinks');
}

function openNewFilterWithHeader(headerName, headerValue, accountName, mailboxName) {
    // Reset the filter editor
    delete document.getElementById('save-filter').dataset.editId;
    document.getElementById('new-filter-title').innerText = 'New Filter';
    document.getElementById('filter-name').value = headerName + ': ' + headerValue;
    editorState.accounts = accountName ? [accountName] : [];
    editorState.mailboxes = mailboxName ? [mailboxName] : [];
    renderTagList('filter-accounts-list', editorState.accounts);
    renderTagList('filter-mailboxes-list', editorState.mailboxes);
    document.getElementById('filter-conditions-type').value = 'and';
    setFilterSchedule(null);

    const condContainer = document.getElementById('filter-conditions');
    condContainer.innerHTML = '';
    const knownFields = ['from', 'sender', 'to', 'cc', 'bcc', 'from/to', 'from/to/cc/bcc', 'subject', 'body', 'age', 'status'];
    let lower = headerName.toLowerCase();
    if (lower.endsWith(':e-mail')) lower = lower.slice(0, -7);
    const field = knownFields.includes(lower) ? lower : 'header:' + headerName;
    const row = makeConditionRow({field: field, operator: 'equals', value: headerValue}, () => {});
    condContainer.appendChild(row);

    const actContainer = document.getElementById('filter-actions');
    actContainer.innerHTML = '';
    const actRow = makeActionRow({type: 'move', value: ''}, () => {});
    actContainer.appendChild(actRow);

    openTab(null, 'new-filter-content', 'mail-tabcontent', 'mail-tablinks');
}
