// labels: function declarations shared globally with the other app/*.js files.

// Sort a list of label definitions by (weight asc, name asc).
function sortedLabelDefs(defs) {
    const out = (defs || []).slice();
    const w = d => (d && typeof d.weight === 'number') ? d.weight : 0;
    out.sort((a, b) => {
        if (w(a) !== w(b)) return w(a) - w(b);
        return (a.name || '').localeCompare(b.name || '');
    });
    return out;
}

async function loadLabelsData() {
    try {
        const res = await fetch('/api/labels', { credentials: 'include' });
        if (res.ok) labelsData = await res.json() || [];
    } catch (e) { /* ignore */ }
}

async function loadLabels() {
    try {
        const res = await fetch('/api/labels', { credentials: 'include' });
        checkResponse(res);
        if (!res.ok) throw new Error('no labels endpoint');
        const labels = await res.json();
        labelsData = labels || [];
        renderFilteredLabels();
    } catch (err) {
        document.getElementById('labels-list').innerText = 'Labels API not available or failed: ' + err.message;
    }
}

function renderFilteredLabels() {
    const q = (document.getElementById('label-search')?.value || '').trim().toLowerCase();
    const matched = q
        ? labelsData.filter(l => [l.name || '', ...(l.accounts || [])].join(' ').toLowerCase().includes(q))
        : labelsData;
    renderLabelsList(matched);
}

function renderLabelsList(labels) {
    const container = document.getElementById('labels-list');
    const count = document.getElementById('labels-count');
    const total = labelsData.length;
    const shown = labels ? labels.length : 0;
    if (count) count.textContent = shown === total ? `${total} label${total === 1 ? '' : 's'}` : `${shown} of ${total} labels`;
    if (!labels || labels.length === 0) {
        container.innerHTML = '<div class="filter-list-empty">No labels defined</div>';
        return;
    }
    container.innerHTML = sortedLabelDefs(labels).map(l => {
        const scope = l.accounts && l.accounts.length
            ? l.accounts.map(a => `<span class="filter-chip">${escapeHtml(a)}</span>`).join('')
            : '<span class="filter-chip filter-chip-muted">all accounts</span>';
        const storage = l.storage === 'imap' ? 'IMAP keyword' : 'local';
        const weight = (typeof l.weight === 'number') ? l.weight : 0;
        const parts = [
            `<span class="filter-card-count">${storage}</span>`,
            `<span>${l.hidden ? 'hidden' : 'visible'}</span>`,
            `<span>weight ${weight}</span>`,
        ];
        return `<div class="filter-card filter-card-compact" data-name="${escapeAttr(l.name)}">
            <div class="filter-card-head">
                <div class="filter-card-name"><span class="label-chip" style="background-color:${escapeAttr(l.color || '#888')}">${escapeHtml(l.name)}</span></div>
                <div class="filter-card-summary">${parts.join('<span class="filter-card-joiner">&middot;</span>')}</div>
                <div class="filter-card-scope">${scope}</div>
                <div class="filter-card-tools">
                    <button class="btn-icon filter-card-delete" data-action="delete-label" data-name="${escapeAttr(l.name)}" title="Delete label" aria-label="Delete label">
                        ${svgDeleteIcon(18)}
                    </button>
                </div>
            </div>
        </div>`;
    }).join('');

    container.querySelectorAll('.filter-card').forEach(card => {
        card.addEventListener('click', (e) => {
            if (e.target.closest('[data-action]')) return;
            openLabelEditor(card.dataset.name);
        });
    });
}

function openLabels(evt) {
    setViewHash('labels');
    openTab(evt, 'labels-content', 'mail-tabcontent', 'mail-tablinks');
    loadLabels();
}

async function openLabelEditor(name) {
    try {
        const res = await fetch('/api/labels/' + encodeURIComponent(name), { credentials: 'include' });
        if (!res.ok) throw new Error('failed to load');
        const label = await res.json();

        document.getElementById('label-name').value = label.name || '';
        document.getElementById('label-color').value = label.color || '#1a73e8';
        const preview = document.getElementById('label-preview');
        preview.style.backgroundColor = label.color || '#1a73e8';
        preview.textContent = label.name || 'Preview';
        labelEditorAccounts = label.accounts ? label.accounts.slice() : [];
        renderTagList('label-accounts-list', labelEditorAccounts);
        document.getElementById('label-storage-imap').checked = (label.storage === 'imap');
        document.getElementById('label-hidden').checked = !!label.hidden;
        document.getElementById('label-weight').value = (typeof label.weight === 'number') ? label.weight : 0;
        document.getElementById('save-label').dataset.editName = name;
        document.getElementById('new-label-title').innerText = 'Edit Label';
        openTab(null, 'new-label-content', 'mail-tabcontent', 'mail-tablinks');
    } catch (err) {
        showToast('Failed to load label: ' + err.message, 'error');
    }
}

async function saveLabel() {
    const editName = document.getElementById('save-label').dataset.editName;
    const name = document.getElementById('label-name').value.trim();
    const color = document.getElementById('label-color').value;
    if (!name) { showToast('Label name is required', 'warning'); return; }

    const storage = document.getElementById('label-storage-imap').checked ? 'imap' : 'local';
    const hidden = document.getElementById('label-hidden').checked;
    const weight = parseInt(document.getElementById('label-weight').value, 10) || 0;
    const payload = { name, color, accounts: labelEditorAccounts.slice(), storage, hidden, weight };
    try {
        let res;
        if (editName) {
            res = await fetch('/api/labels/' + encodeURIComponent(editName), { method: 'PUT', credentials: 'include', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
        } else {
            res = await fetch('/api/labels', { method: 'POST', credentials: 'include', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
        }
        if (!res.ok) {
            const errText = await res.text();
            throw new Error(errText || 'save failed');
        }
        delete document.getElementById('save-label').dataset.editName;
        document.getElementById('new-label-title').innerText = 'New Label';
        openTab(null, 'labels-content', 'mail-tabcontent', 'mail-tablinks');
        loadLabels();
    } catch (err) {
        showToast('Save failed: ' + err.message, 'error');
    }
}

function deleteLabelWrapper(event, name) {
    event.stopPropagation();
    deleteLabel(name);
}

async function deleteLabel(name) {
    if (!(await showConfirm('Delete label "' + name + '"?', 'Delete'))) return;
    try {
        const res = await fetch('/api/labels/' + encodeURIComponent(name), { method: 'DELETE', credentials: 'include' });
        checkResponse(res);
        if (!res.ok) throw new Error('delete failed');
        loadLabels();
    } catch (err) {
        showToast('Delete failed: ' + err.message, 'error');
    }
}

function openNewLabel() {
    delete document.getElementById('save-label').dataset.editName;
    document.getElementById('new-label-title').innerText = 'New Label';
    document.getElementById('label-name').value = '';
    document.getElementById('label-color').value = '#1a73e8';
    document.getElementById('label-preview').style.backgroundColor = '#1a73e8';
    document.getElementById('label-preview').textContent = 'Preview';
    labelEditorAccounts = [];
    renderTagList('label-accounts-list', labelEditorAccounts);
    document.getElementById('label-storage-imap').checked = false;
    document.getElementById('label-hidden').checked = false;
    document.getElementById('label-weight').value = 0;
    openTab(null, 'new-label-content', 'mail-tabcontent', 'mail-tablinks');
}

async function removeEmailLabel(accountName, mailboxName, uid, labelName, mailContent) {
    const res = await fetch('/api/mail/label', { method: 'POST', credentials: 'include', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ account: accountName, mailbox: mailboxName, uid: parseInt(uid), label: labelName, action: 'remove' }) });
    if (res.ok) {
        const data = await res.json();
        mailContent.labels = data.labels;
        mailContent.flags = data.flags;
        const container = document.getElementById('email-labels');
        if (container) {
            container.innerHTML = '';
            const addRemovableChip = (l, color, title) => {
                const chip = document.createElement('span');
                chip.className = 'label-chip label-chip-removable';
                chip.style.backgroundColor = color;
                chip.dataset.label = l;
                if (title) chip.title = title;
                chip.innerHTML = DOMPurify.sanitize(l) + '<span class="label-chip-x">&times;</span>';
                chip.onclick = () => removeEmailLabel(accountName, mailboxName, uid, l, mailContent);
                container.appendChild(chip);
            };
            orderedChipsFor(data.labels, data.flags).forEach(item => {
                if (item.kind === 'orphan') {
                    const chip = document.createElement('span');
                    chip.className = 'flag-chip flag-chip-removable';
                    chip.dataset.label = item.name;
                    chip.title = 'IMAP flag';
                    chip.innerHTML = DOMPurify.sanitize(item.name) + '<span class="label-chip-x">&times;</span>';
                    chip.onclick = () => removeEmailLabel(accountName, mailboxName, uid, item.name, mailContent);
                    container.appendChild(chip);
                } else {
                    const color = (item.def && item.def.color) || '#888';
                    const title = item.kind === 'imap' ? 'IMAP keyword (defined label)' : undefined;
                    addRemovableChip(item.name, color, title);
                }
            });
        }
    }
}

async function fetchLabelNames() {
    if (labelNamesCache !== null) return labelNamesCache;
    try {
        const res = await fetch('/api/labels', { credentials: 'include' });
        if (!res.ok) { labelNamesCache = []; return labelNamesCache; }
        const data = await res.json();
        labelNamesCache = (data || []).map(l => l.name || l.Name).filter(Boolean);
    } catch (e) {
        labelNamesCache = [];
    }
    return labelNamesCache;
}
