// mail: function declarations shared globally with the other app/*.js files.

function showToast(message, type = 'info', duration = 4000) {
    const container = document.getElementById('toast-container');
    const toast = document.createElement('div');
    toast.className = 'toast toast-' + type;

    const icons = {
        success: '<svg class="toast-icon" viewBox="0 0 24 24" fill="currentColor"><path d="M9 16.17L4.83 12l-1.42 1.41L9 19 21 7l-1.41-1.41z"/></svg>',
        error: '<svg class="toast-icon" viewBox="0 0 24 24" fill="currentColor"><path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-2h2v2zm0-4h-2V7h2v6z"/></svg>',
        warning: '<svg class="toast-icon" viewBox="0 0 24 24" fill="currentColor"><path d="M1 21h22L12 2 1 21zm12-3h-2v-2h2v2zm0-4h-2v-4h2v4z"/></svg>',
        info: '<svg class="toast-icon" viewBox="0 0 24 24" fill="currentColor"><path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-6h2v6zm0-8h-2V7h2v2z"/></svg>'
    };

    toast.innerHTML = (icons[type] || icons.info)
        + '<span class="toast-message">' + DOMPurify.sanitize(message) + '</span>'
        + '<button class="toast-close" data-action="toast-close">&times;</button>';

    container.appendChild(toast);

    if (duration > 0) {
        setTimeout(() => {
            toast.classList.add('toast-out');
            setTimeout(() => toast.remove(), 250);
        }, duration);
    }
}

function showConfirm(message, okLabel) {
    if (!okLabel) okLabel = 'Confirm';
    return new Promise((resolve) => {
        const overlay = document.createElement('div');
        overlay.className = 'confirm-overlay';
        const dialog = document.createElement('div');
        dialog.className = 'confirm-dialog';
        dialog.innerHTML = '<div class="confirm-message">' + DOMPurify.sanitize(message) + '</div>'
            + '<div class="confirm-actions">'
            + '<button class="confirm-btn confirm-cancel">Cancel</button>'
            + '<button class="confirm-btn confirm-ok">' + DOMPurify.sanitize(okLabel) + '</button>'
            + '</div>';
        overlay.appendChild(dialog);
        document.body.appendChild(overlay);
        requestAnimationFrame(() => overlay.classList.add('visible'));

        function close(result) {
            overlay.classList.remove('visible');
            setTimeout(() => overlay.remove(), 200);
            resolve(result);
        }
        overlay.querySelector('.confirm-cancel').onclick = () => close(false);
        overlay.querySelector('.confirm-ok').onclick = () => close(true);
        overlay.addEventListener('click', (e) => { if (e.target === overlay) close(false); });
        overlay.querySelector('.confirm-ok').focus();
    });
}

async function fetchAccounts() {
    const response = await fetch('/api/accounts', { credentials: 'include' });
    checkResponse(response);
    const accounts = await response.json();
    return accounts;
}

async function fetchMailboxes(accountName) {
    const response = await fetch(`/api/mailboxes?account=${encodeURIComponent(accountName)}`, { credentials: 'include' });
    checkResponse(response);
    const mailboxes = await response.json();
    return mailboxes;
}

async function fetchMailboxStatus(accountName, mailboxName) {
    const response = await fetch(`/api/mailbox?account=${encodeURIComponent(accountName)}&mailbox=${encodeURIComponent(mailboxName)}`, { credentials: 'include' });
    checkResponse(response);
    const status = await response.json();
    return status;
}

async function fetchMails(accountName, mailboxName, page) {
    const p = page || 1;
    const ps = getPageSize();
    let url = `/api/mails?account=${encodeURIComponent(accountName)}&mailbox=${encodeURIComponent(mailboxName)}&page=${p}&pageSize=${ps}`;
    if (showUnreadOnly) url += '&unread=true';
    const response = await fetch(url, { credentials: 'include' });
    checkResponse(response);
    const data = await response.json();
    return data;
}

async function fetchMail(accountName, mailboxName, uid) {
    const response = await fetch(`/api/mail?account=${encodeURIComponent(accountName)}&mailbox=${encodeURIComponent(mailboxName)}&uid=${uid}`, { credentials: 'include' });
    checkResponse(response);
    const mail = await response.json();
    return mail;
}

function toggleThread(evt, threadId) {
    evt.stopPropagation();
    const rows = document.getElementsByClassName(`thread-child-${threadId}`);
    let isExpanded = false;
    for (let row of rows) {
        if (row.style.display === 'none') {
            row.style.display = 'flex';
            isExpanded = true;
        } else {
            row.style.display = 'none';
            isExpanded = false;
        }
    }
    const icon = evt.currentTarget;
    icon.innerHTML = isExpanded ? '&#9660;' : '&#9654;';
    // Persist thread state (default is collapsed, so store when explicitly expanded)
    const key = getThreadStateKey(currentMailbox.account, currentMailbox.mailbox, threadId);
    const states = loadThreadStates();
    if (isExpanded) {
        states[key] = true; // true = explicitly expanded
    } else {
        delete states[key]; // collapsed is default, no need to store
    }
    saveThreadStates(states);
}

function buildMailboxTree(mailboxes, accountName, mailboxSettings, order) {
    const delimiters = ['/', '.'];

    const settingsMap = {};
    if (mailboxSettings) {
        mailboxSettings.forEach(setting => {
            if (setting && setting.Mailbox) {
                settingsMap[setting.Mailbox.toLowerCase()] = setting.Icon;
            }
        });
    }

    function getIconHtml(fullName) {
        const icon = settingsMap[fullName.toLowerCase()] || '📂';
        if ([...icon].length > 1) {
            return `<img src="${icon}" alt="" class="mailbox-icon-img">`;
        }
        return icon + ' ';
    }

    function renderSpan(fullName, displayName) {
        const iconHtml = getIconHtml(fullName);
        return `<span data-account="${accountName}" data-mailbox="${fullName}">${iconHtml}<span class="tree-label">${displayName}</span></span>`;
    }

    function detectDelimiter(name) {
        for (const d of delimiters) {
            if (name.includes(d)) return d;
        }
        return '';
    }

    function getParent(name) {
        const d = detectDelimiter(name);
        if (!d) return null;
        const idx = name.lastIndexOf(d);
        return { parent: name.substring(0, idx), child: name.substring(idx + 1), delimiter: d };
    }

    // Build alphabetical tree (used for remaining mailboxes and as fallback)
    function buildAlphabeticalTree(mboxList) {
        const root = { children: {}, name: "" };
        mboxList.forEach(mailbox => {
            let parts = [mailbox];
            let delimiter = '';
            for (const d of delimiters) {
                if (mailbox.includes(d)) {
                    parts = mailbox.split(d);
                    delimiter = d;
                    break;
                }
            }
            let node = root;
            let currentPath = '';
            parts.forEach((part, i) => {
                currentPath = i === 0 ? part : currentPath + delimiter + part;
                if (!node.children[part]) {
                    node.children[part] = { children: {}, name: part, fullName: currentPath };
                }
                node = node.children[part];
            });
        });

        function buildHTML(node, isRoot) {
            const keys = Object.keys(node.children).sort();
            if (keys.length === 0) return '';
            let html = isRoot ? '' : '<ul class="tree-children">';
            for (const key of keys) {
                const child = node.children[key];
                html += `<li>${renderSpan(child.fullName, child.name)}${buildHTML(child, false)}</li>`;
            }
            html += isRoot ? '' : '</ul>';
            return html;
        }
        return buildHTML(root, true);
    }

    // No order: fall back to alphabetical
    if (!order || order.length === 0) {
        return `<ul class="tree-root">${buildAlphabeticalTree(mailboxes)}</ul>`;
    }

    // Build blocks: groups of consecutive order items sharing the same parent
    const allMailboxSet = new Set(mailboxes);
    const orderedSet = new Set(order);
    const blocks = [];
    for (const item of order) {
        if (!allMailboxSet.has(item)) continue;
        const info = getParent(item);
        const parentName = info ? info.parent : null;
        const lastBlock = blocks.length > 0 ? blocks[blocks.length - 1] : null;
        if (lastBlock && lastBlock.parent === parentName) {
            lastBlock.items.push(item);
        } else {
            blocks.push({ parent: parentName, items: [item] });
        }
    }

    // Collect remaining mailboxes (not in order)
    const remaining = mailboxes.filter(m => !orderedSet.has(m));

    // Also remove parent-only nodes that are created implicitly by ordered children
    // (they'll be rendered as part of the ordered blocks)
    const implicitParents = new Set();
    for (const block of blocks) {
        if (block.parent) implicitParents.add(block.parent);
    }

    // Filter remaining: exclude mailboxes whose children are ALL accounted for in order
    // but keep mailboxes that have remaining (unordered) children or are standalone
    const remainingFiltered = remaining.filter(m => {
        // If this mailbox is an implicit parent AND has no remaining children, skip it
        if (implicitParents.has(m)) {
            const hasRemainingChildren = remaining.some(r => {
                const info = getParent(r);
                return info && info.parent === m;
            });
            if (!hasRemainingChildren) return false;
        }
        return true;
    });

    let html = '<ul class="tree-root">';

    // Render ordered blocks
    for (const block of blocks) {
        if (block.parent === null) {
            // Top-level item(s)
            for (const item of block.items) {
                html += `<li>${renderSpan(item, item)}`;
                // Render any children of this item that aren't explicitly ordered elsewhere
                const childMailboxes = remaining.filter(m => {
                    const info = getParent(m);
                    return info && info.parent === item;
                });
                if (childMailboxes.length > 0) {
                    html += '<ul class="tree-children">';
                    childMailboxes.sort((a, b) => a.localeCompare(b));
                    for (const child of childMailboxes) {
                        const info = getParent(child);
                        html += `<li>${renderSpan(child, info.child)}</li>`;
                    }
                    html += '</ul>';
                }
                html += '</li>';
            }
        } else {
            // Children grouped under a parent
            html += `<li>${renderSpan(block.parent, block.parent)}`;
            html += '<ul class="tree-children">';
            for (const item of block.items) {
                const info = getParent(item);
                html += `<li>${renderSpan(item, info.child)}</li>`;
            }
            html += '</ul></li>';
        }
    }

    // Render remaining mailboxes alphabetically
    remainingFiltered.sort((a, b) => a.localeCompare(b));
    html += buildAlphabeticalTree(remainingFiltered);

    html += '</ul>';
    return html;
}

// Fill an element with text, wrapping the [start, start+len) slice in <mark>.
// Builds DOM nodes so attacker-controlled text can never inject markup.
function renderHighlightedText(el, text, start, len) {
    el.textContent = '';
    if (start < 0) { el.textContent = text; return; }
    el.appendChild(document.createTextNode(text.slice(0, start)));
    const mark = document.createElement('mark');
    mark.textContent = text.slice(start, start + len);
    el.appendChild(mark);
    el.appendChild(document.createTextNode(text.slice(start + len)));
}

function loadThreadStates() {
try {
    return JSON.parse(localStorage.getItem(THREAD_STATE_KEY)) || {};
} catch { return {}; }
    }

function saveThreadStates(states) {
localStorage.setItem(THREAD_STATE_KEY, JSON.stringify(states));
    }

function getThreadStateKey(account, mailbox, threadId) {
return `${account}|${mailbox}|${threadId}`;
    }

function calcAutoPageSize() {
const isMobile = window.matchMedia('(max-width: 600px)').matches;
// toolbar 48 + mailbox toolbar ~36 (none on mobile) + pagination ~36 + slack 8
const overhead = isMobile ? 48 + 36 + 8 : 48 + 36 + 36 + 8;
const rowHeight = isMobile ? 64 : 36;
const available = window.innerHeight - overhead;
return Math.max(10, Math.floor(available / rowHeight));
    }

function getPageSize() {
if (PAGE_SIZE > 0) return PAGE_SIZE;
return calcAutoPageSize();
    }

function updateSelectionUI() {
const deleteBtn = document.getElementById('delete-selected-button');
const markReadBtn = document.getElementById('mark-selected-read-button');
const moveWrapper = document.getElementById('move-selected-wrapper');
const filterBtn = document.getElementById('filter-selected-button');
const spamBtn = document.getElementById('spam-selected-button');
const trainSpamBtn = document.getElementById('train-spam-selected-button');
const selectAllCb = document.getElementById('select-all-checkbox');
const count = selectedUIDs.size;
const hasSelection = count > 0 || selectAllInMailbox;
if (deleteBtn) deleteBtn.style.display = hasSelection ? 'inline-flex' : 'none';
if (markReadBtn) markReadBtn.style.display = hasSelection ? 'inline-flex' : 'none';
if (moveWrapper) moveWrapper.style.display = hasSelection ? 'inline-flex' : 'none';
if (filterBtn) filterBtn.style.display = hasSelection ? 'inline-flex' : 'none';
const isJunk = currentMailbox.mailbox && currentMailbox.mailbox.toLowerCase() === spamMailboxName.toLowerCase();
if (spamBtn) {
    spamBtn.style.display = hasSelection ? 'inline-flex' : 'none';
    if (isJunk) {
        spamBtn.title = 'Mark selected as not spam';
        spamBtn.innerHTML = '<svg width="20" height="20" viewBox="0 0 24 24" fill="#188038"><path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-2 15l-5-5 1.41-1.41L10 14.17l7.59-7.59L19 8l-9 9z"/></svg>';
    } else {
        spamBtn.title = 'Report selected as spam';
        spamBtn.innerHTML = '<svg width="20" height="20" viewBox="0 0 24 24" fill="#d93025"><path d="M1 21h22L12 2 1 21zm12-3h-2v-2h2v2zm0-4h-2v-4h2v4z"/></svg>';
    }
}
if (trainSpamBtn) {
    trainSpamBtn.style.display = (hasSelection && isJunk) ? 'inline-flex' : 'none';
}
const trainHamBtn = document.getElementById('train-ham-selected-button');
if (trainHamBtn) {
    trainHamBtn.style.display = (hasSelection && !isJunk) ? 'inline-flex' : 'none';
}
if (selectAllCb) {
    const allCheckboxes = document.querySelectorAll('.mail-select-checkbox:not(#select-all-checkbox)');
    selectAllCb.checked = selectAllInMailbox || (allCheckboxes.length > 0 && count === allCheckboxes.length);
    selectAllCb.indeterminate = !selectAllInMailbox && count > 0 && count < allCheckboxes.length;
}
    }

function buildCanonicalHash(account, mailbox, uid) {
    const parts = [];
    if (account) parts.push('account=' + encodeURIComponent(account));
    if (mailbox) parts.push('mailbox=' + encodeURIComponent(mailbox));
    if (uid) parts.push('uid=' + encodeURIComponent(uid));
    return parts.length ? '#' + parts.join('&') : '';
}

function setNormalizedHash(account, mailbox, uid) {
    const newHash = buildCanonicalHash(account, mailbox, uid);
    if (newHash !== window.location.hash) {
        window.location.hash = newHash;
    }
}

// Standalone views (filters/labels/plugins) routed via #view=
function setViewHash(view) {
    const newHash = '#view=' + encodeURIComponent(view);
    if (newHash !== window.location.hash) {
        window.location.hash = newHash;
    }
}

function showMails(evt) {
    const hash = window.location.hash.substring(1);
    const params = new URLSearchParams(hash);
    if (params.has('view')) {
        // Leaving a standalone view back to the current mailbox.
        setNormalizedHash(currentMailbox.account || '', currentMailbox.mailbox || '', '');
    } else if (params.has('uid')) {
        params.delete('uid');
        const a = params.get('account') || '';
        const m = params.get('mailbox') || '';
        setNormalizedHash(a, m, '');
    }
    openTab(evt, 'mails-container', 'mail-tabcontent', 'mail-tablinks');
}

function updateMailboxCountBadge(span, status) {
    if (!status) return;
    const existingCount = span.querySelector('.unread-count');
    if (existingCount) existingCount.remove();
    const existingSpace = span.querySelector('.unread-count-space');
    if (existingSpace) existingSpace.remove();
    const existingTotal = span.querySelector('.total-count');
    if (existingTotal) existingTotal.remove();

    if (showMailboxCounts) {
        const totalSpan = document.createElement('span');
        totalSpan.className = 'total-count';
        if (status.Unseen > 0) {
            totalSpan.innerHTML = `<b>${status.Unseen}</b>/${status.Messages}`;
        } else {
            totalSpan.textContent = status.Messages;
        }
        span.appendChild(totalSpan);
        span.style.fontWeight = 'normal';
    } else {
        if (status.Unseen > 0) {
            const countSpanSpace = document.createElement('span');
            countSpanSpace.className = 'unread-count-space';
            span.appendChild(countSpanSpace);
            const countSpan = document.createElement('span');
            countSpan.className = 'unread-count';
            countSpan.innerText = status.Unseen;
            span.appendChild(countSpan);
            span.style.fontWeight = 'bold';
        } else {
            span.style.fontWeight = 'normal';
        }
    }
}

async function refreshAllMailboxCounts() {
    const accounts = accountsData;
    for (const account of accounts) {
        const mailboxData = await fetchMailboxes(account.Name);
        for (const mailbox of mailboxData.mailboxes) {
            const status = await fetchMailboxStatus(account.Name, mailbox);
            const spans = document.querySelectorAll(`span[data-account="${account.Name}"][data-mailbox="${mailbox}"]`);
            spans.forEach(span => updateMailboxCountBadge(span, status));
        }
    }
}

async function waitForReady() {
    const overlay = document.getElementById('loading-overlay');
    const statusEl = document.getElementById('loading-status');
    const bar = document.getElementById('loading-bar');
    const statusMessages = {
        loading: 'Loading cache...',
        syncing: 'Syncing emails from IMAP...',
        filtering: 'Running filters...',
        ready: 'Ready!',
    };
    const statusProgress = { loading: '15%', syncing: '40%', filtering: '75%', ready: '100%' };
    try {
        // Skip the blocking overlay if startup already finished once.
        // Subsequent periodic syncs are surfaced via the filters-running indicator instead.
        try {
            const fs = await fetch('/api/filters/status', { credentials: 'include' });
            if (fs.ok) {
                const fd = await fs.json();
                if (fd && fd.have_run) return;
            }
        } catch (_) { /* fall through to status-based wait */ }
        const res = await fetch('/api/sync-status', { credentials: 'include' });
        if (!res.ok) return; // not logged in or error, skip
        const data = await res.json();
        if (data.status === 'ready') return;
        overlay.style.display = 'flex';
        statusEl.textContent = statusMessages[data.status] || 'Loading...';
        bar.style.width = statusProgress[data.status] || '10%';
        while (true) {
            await new Promise(r => setTimeout(r, 1000));
            try {
                const r2 = await fetch('/api/sync-status', { credentials: 'include' });
                if (!r2.ok) break;
                const d2 = await r2.json();
                statusEl.textContent = statusMessages[d2.status] || d2.status;
                bar.style.width = statusProgress[d2.status] || '10%';
                if (d2.status === 'ready') {
                    await new Promise(r => setTimeout(r, 400));
                    break;
                }
            } catch (_) { break; }
        }
        overlay.style.display = 'none';
    } catch (_) { /* not logged in, skip */ }
}

async function renderMails(accountName, mailboxName, mailsData, page, preserveSelection) {
    if (page) currentPage = page;
    const prevSelected = preserveSelection ? new Set(selectedUIDs) : null;
    const prevSelectAll = preserveSelection ? selectAllInMailbox : false;
    selectedUIDs.clear();
    selectAllInMailbox = false;
    let mails = null;
    let totalConversations = 0;
    let totalMessages = 0; // actual email count from mailbox status
    let pageSize = PAGE_SIZE;
    let currentPg = currentPage;

    if (mailsData && mailsData.conversations !== undefined) {
        mails = mailsData.conversations;
        totalConversations = mailsData.total;
        pageSize = mailsData.pageSize;
        currentPg = mailsData.page;
        currentPage = currentPg;
    } else if (Array.isArray(mailsData)) {
        mails = mailsData;
        totalConversations = mailsData.length;
    }
    const mailsContainer = document.getElementById('mails-container');

    const mbIcon = mailboxSettingsMap[mailboxName.toLowerCase()] || '';
    let mbIconHtml = '';
    if (mbIcon) {
        if ([...mbIcon].length > 1) {
            mbIconHtml = `<img src="${mbIcon}" alt="" class="mailbox-icon-img">`;
        } else {
            mbIconHtml = mbIcon + ' ';
        }
    }

    mailsContainer.innerHTML = `
        <div class="mailbox-toolbar">
            <div class="mailbox-header">
                <span class="mailbox-name">${mbIconHtml}${mailboxName}</span>
                <span class="mailbox-stats"></span>
            </div>
            <div class="mailbox-toolbar-right">
                <label class="toggle-label" style="display: flex; align-items: center; cursor: pointer;">
                    <div class="toggle-switch">
                        <input type="checkbox" id="show-spam-scores-checkbox" ${showSpamScores ? 'checked' : ''}>
                        <span class="slider"></span>
                    </div>
                    Spam scores
                </label>
                <label class="toggle-label" style="display: flex; align-items: center; cursor: pointer;">
                    <div class="toggle-switch">
                        <input type="checkbox" id="show-unread-checkbox" ${showUnreadOnly ? 'checked' : ''}>
                        <span class="slider"></span>
                    </div>
                    Unread only
                </label>
                <button id="train-junk-button" class="btn btn-sm" style="display: none;" title="Train all emails in this mailbox as spam for the Bayesian filter">Train as spam</button>
                <button id="delete-mailbox-button" class="btn-icon" style="display: none;" title="Delete Mailbox">
                    <svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M6 19c0 1.1.9 2 2 2h8c1.1 0 2-.9 2-2V7H6v12zM19 4h-3.5l-1-1h-5l-1 1H5v2h14V4z"/></svg>
                </button>
                <div id="pagination-controls" class="pagination-bar"></div>
            </div>
        </div>
        <div id="refresh-indicator" class="refresh-indicator" style="display:none;">Refreshing...</div>
        <div id="select-all-banner" class="select-all-banner" style="display:none;"></div>
        <div id="mail-list-scroll" class="mail-list-scroll"></div>
        <div id="pagination-bottom" class="pagination-bar pagination-bottom">
            <input type="checkbox" id="select-all-checkbox" class="mail-select-checkbox" title="Select all">
            <button id="delete-selected-button" class="btn-icon" title="Delete selected" style="display:none;">
                <svg width="20" height="20" viewBox="0 0 24 24" fill="#d93025"><path d="M6 19c0 1.1.9 2 2 2h8c1.1 0 2-.9 2-2V7H6v12zM19 4h-3.5l-1-1h-5l-1 1H5v2h14V4z"/></svg>
            </button>
            <button id="mark-selected-read-button" class="btn-icon" title="Mark selected as read" style="display:none;">
                <svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M18 7l-1.41-1.41-6.34 6.34 1.41 1.41L18 7zm4.24-1.41L11.66 16.17 7.48 12l-1.41 1.41L11.66 19l12-12-1.42-1.41zM.41 13.41L6 19l1.41-1.41L1.83 12 .41 13.41z"/></svg>
            </button>
            <div id="move-selected-wrapper" style="display:none;position:relative;">
                <button id="move-selected-button" class="btn-icon" title="Move selected to...">
                    <svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M20 6h-8l-2-2H4c-1.1 0-1.99.9-1.99 2L2 18c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V8c0-1.1-.9-2-2-2zm0 12H4V8h16v10z"/></svg>
                </button>
                <select id="move-selected-select" style="position:absolute;opacity:0;width:100%;height:100%;top:0;left:0;cursor:pointer;"></select>
            </div>
            <button id="filter-selected-button" class="btn-icon" title="Run filters on selected" style="display:none;">
                <svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M10 18h4v-2h-4v2zM3 6v2h18V6H3zm3 7h12v-2H6v2z"/></svg>
            </button>
            <button id="spam-selected-button" class="btn-icon" title="Report selected as spam" style="display:none;">
                <svg width="20" height="20" viewBox="0 0 24 24" fill="#d93025"><path d="M1 21h22L12 2 1 21zm12-3h-2v-2h2v2zm0-4h-2v-4h2v4z"/></svg>
            </button>
            <button id="train-spam-selected-button" class="btn-icon" title="Train selected as spam" style="display:none;">
                <svg width="20" height="20" viewBox="0 0 24 24" fill="#f9ab00"><path d="M1 21h22L12 2 1 21zm12-3h-2v-2h2v2zm0-4h-2v-4h2v4z"/></svg>
            </button>
            <button id="train-ham-selected-button" class="btn-icon" title="Train selected as not spam" style="display:none;">
                <svg width="20" height="20" viewBox="0 0 24 24" fill="#188038"><path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-2 15l-5-5 1.41-1.41L10 14.17l7.59-7.59L19 8l-9 9z"/></svg>
            </button>
            <div class="pagination-spacer"></div>
        </div>
    `;

    const status = await fetchMailboxStatus(accountName, mailboxName);
    totalMessages = status.Messages || 0;
    document.querySelector('.mailbox-name').innerHTML = mbIconHtml + mailboxName;
    document.querySelector('.mailbox-stats').innerText = `${status.Unseen}/${status.Messages}`;
    const deleteMailboxBtn = document.getElementById('delete-mailbox-button');
    if (status.Messages === 0) {
        deleteMailboxBtn.style.display = 'inline-flex';
    } else {
        deleteMailboxBtn.style.display = 'none';
    }

    // Show "Train as spam" button when viewing the Junk mailbox
    const trainJunkBtn = document.getElementById('train-junk-button');
    if (mailboxName.toLowerCase() === spamMailboxName.toLowerCase() && status.Messages > 0) {
        trainJunkBtn.style.display = 'inline-block';
    } else {
        trainJunkBtn.style.display = 'none';
    }

    document.getElementById('show-unread-checkbox').addEventListener('change', (e) => {
        showUnreadOnly = e.target.checked;
        currentPage = 1;
        renderMails(accountName, mailboxName, null, 1);
    });

    document.getElementById('show-spam-scores-checkbox').addEventListener('change', (e) => {
        showSpamScores = e.target.checked;
        localStorage.setItem('go-mail-spam-scores', showSpamScores);
        renderMails(accountName, mailboxName, null, currentPage);
    });

    document.getElementById('delete-mailbox-button').addEventListener('click', async () => {
        if (await showConfirm(`Are you sure you want to delete the mailbox "${mailboxName}"?`, 'Delete')) {
            try {
                const response = await fetch(`/api/mailbox?account=${encodeURIComponent(accountName)}&mailbox=${encodeURIComponent(mailboxName)}`, {
                    method: 'DELETE',
                    credentials: 'include'
                });
                checkResponse(response);
                if (response.ok) {
                    const accountTab = Array.from(document.querySelectorAll('.account-tablinks')).find(tab => tab.dataset.account === accountName);
                    if (accountTab) accountTab.click();
                } else {
                    const error = await response.text();
                    showToast('Failed to delete mailbox: ' + error, 'error');
                }
            } catch (err) {
                showToast('An error occurred: ' + err.message, 'error');
            }
        }
    });

    document.getElementById('train-junk-button').addEventListener('click', async () => {
        if (!await showConfirm(`Train all ${totalMessages} email(s) in ${mailboxName} as spam?`, 'Train')) return;
        const btn = document.getElementById('train-junk-button');
        btn.disabled = true;
        btn.textContent = 'Training...';
        try {
            const res = await fetch('/api/spam/train-batch', {
                method: 'POST', credentials: 'include',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ account: accountName, mailbox: mailboxName, all: true, spam: true })
            });
            checkResponse(res);
            if (res.ok) {
                const data = await res.json();
                showToast(`Trained ${data.trained} email(s) as spam`, 'success');
            }
        } finally {
            btn.disabled = false;
            btn.textContent = 'Train as spam';
        }
    });

    document.getElementById('select-all-checkbox').addEventListener('change', (e) => {
        const allCheckboxes = document.querySelectorAll('.mail-select-checkbox:not(#select-all-checkbox)');
        allCheckboxes.forEach(cb => {
            cb.checked = e.target.checked;
            const row = cb.closest('.mail-row');
            if (e.target.checked) {
                selectedUIDs.add(cb.dataset.uid);
                if (row) row.classList.add('checked');
            } else {
                selectedUIDs.delete(cb.dataset.uid);
                if (row) row.classList.remove('checked');
            }
        });
        selectAllInMailbox = false;
        const banner = document.getElementById('select-all-banner');
        if (e.target.checked && totalConversations > allCheckboxes.length) {
            banner.innerHTML = `All ${allCheckboxes.length} conversations on this page are selected. <a href="#" id="select-all-mailbox-link">Select all ${totalMessages} emails in ${mailboxName}</a>`;
            banner.style.display = 'block';
            document.getElementById('select-all-mailbox-link').addEventListener('click', (evt) => {
                evt.preventDefault();
                selectAllInMailbox = true;
                banner.innerHTML = `All ${totalMessages} emails in ${mailboxName} are selected. <a href="#" id="clear-all-mailbox-link">Clear selection</a>`;
                document.getElementById('clear-all-mailbox-link').addEventListener('click', (evt2) => {
                    evt2.preventDefault();
                    selectAllInMailbox = false;
                    banner.style.display = 'none';
                    document.getElementById('select-all-checkbox').checked = false;
                    document.getElementById('select-all-checkbox').dispatchEvent(new Event('change'));
                });
                updateSelectionUI();
            });
        } else {
            banner.style.display = 'none';
        }
        updateSelectionUI();
    });

    document.getElementById('delete-selected-button').addEventListener('click', async () => {
        if (selectedUIDs.size === 0 && !selectAllInMailbox) return;
        const label = selectAllInMailbox ? `all ${totalMessages} emails in ${mailboxName}` : `${selectedUIDs.size} selected email(s)`;
        if (await showConfirm(`Delete ${label}?`, 'Delete')) {
            const payload = { account: accountName, mailbox: mailboxName };
            if (selectAllInMailbox) { payload.all = true; } else { payload.uids = [...selectedUIDs].map(Number); }
            const res = await fetch('/api/mails/delete-selected', {
                method: 'POST',
                credentials: 'include',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload),
            });
            checkResponse(res);
            if (!res.ok) {
                showToast('Failed to delete selected emails: ' + await res.text(), 'error');
                return;
            }
            selectAllInMailbox = false;
            selectedUIDs.clear();
            renderMails(accountName, mailboxName, null, 1);
            refreshSidebarCounts(accountName);
        }
    });

    document.getElementById('mark-selected-read-button').addEventListener('click', async () => {
        if (selectedUIDs.size === 0 && !selectAllInMailbox) return;
        const payload = { account: accountName, mailbox: mailboxName };
        if (selectAllInMailbox) { payload.all = true; } else { payload.uids = [...selectedUIDs].map(Number); }
        const res = await fetch('/api/mails/mark-selected-as-read', {
            method: 'POST',
            credentials: 'include',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload),
        });
        checkResponse(res);
        if (!res.ok) {
            showToast('Failed to mark as read: ' + await res.text(), 'error');
            return;
        }
        selectAllInMailbox = false;
        selectedUIDs.clear();
        renderMails(accountName, mailboxName, null, currentPage);
        refreshSidebarCounts(accountName);
    });

    document.getElementById('filter-selected-button').addEventListener('click', async () => {
        if (selectedUIDs.size === 0 && !selectAllInMailbox) return;
        const btn = document.getElementById('filter-selected-button');
        btn.disabled = true;
        btn.title = 'Running filters...';
        const payload = { account: accountName, mailbox: mailboxName };
        if (selectAllInMailbox) { payload.all = true; } else { payload.uids = [...selectedUIDs].map(Number); }
        try {
            const res = await fetch('/api/filters/run-selected', {
                method: 'POST',
                credentials: 'include',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload),
            });
            checkResponse(res);
            if (!res.ok) {
                showToast('Failed to run filters: ' + await res.text(), 'error');
                return;
            }
            selectedUIDs.clear();
            renderMails(accountName, mailboxName, null, currentPage);
        } finally {
            btn.disabled = false;
            btn.title = 'Run filters on selected';
        }
    });

    document.getElementById('spam-selected-button').addEventListener('click', async () => {
        if (selectedUIDs.size === 0 && !selectAllInMailbox) return;
        const isJunk = mailboxName.toLowerCase() === spamMailboxName.toLowerCase();
        const count = selectAllInMailbox ? totalMessages : selectedUIDs.size;
        const label = isJunk ? 'not spam' : 'spam';

        if (!await showConfirm(`Report ${count} email(s) as ${label}?`, 'Report')) return;

        // Train batch
        const trainPayload = { account: accountName, mailbox: mailboxName, spam: !isJunk };
        if (selectAllInMailbox) { trainPayload.all = true; } else { trainPayload.uids = [...selectedUIDs].map(Number); }
        const trainRes = await fetch('/api/spam/train-batch', {
            method: 'POST', credentials: 'include',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(trainPayload),
        });
        checkResponse(trainRes);

        // Move batch
        const toMailbox = isJunk ? 'INBOX' : spamMailboxName;
        const movePayload = { account: accountName, mailbox: mailboxName, to_mailbox: toMailbox };
        if (selectAllInMailbox) { movePayload.all = true; } else { movePayload.uids = [...selectedUIDs].map(Number); }
        const moveRes = await fetch('/api/mails/move-selected', {
            method: 'POST', credentials: 'include',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(movePayload),
        });
        checkResponse(moveRes);

        selectedUIDs.clear();
        selectAllInMailbox = false;
        renderMails(accountName, mailboxName, null, currentPage);
        refreshSidebarCounts(accountName);
        showToast(`${count} email(s) reported as ${label}`, 'success');
    });

    document.getElementById('train-spam-selected-button').addEventListener('click', async () => {
        if (selectedUIDs.size === 0 && !selectAllInMailbox) return;
        const count = selectAllInMailbox ? totalMessages : selectedUIDs.size;

        if (!await showConfirm(`Train ${count} email(s) as spam (without moving)?`, 'Train')) return;

        const payload = { account: accountName, mailbox: mailboxName, spam: true };
        if (selectAllInMailbox) { payload.all = true; } else { payload.uids = [...selectedUIDs].map(Number); }
        const res = await fetch('/api/spam/train-batch', {
            method: 'POST', credentials: 'include',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload),
        });
        checkResponse(res);
        if (res.ok) {
            const data = await res.json();
            showToast(`Trained ${data.trained} email(s) as spam`, 'success');
        }
        selectedUIDs.clear();
        selectAllInMailbox = false;
        updateSelectionUI();
        document.getElementById('select-all-checkbox').checked = false;
        document.querySelectorAll('.mail-select-checkbox').forEach(cb => { cb.checked = false; });
        document.querySelectorAll('.mail-row.checked').forEach(r => r.classList.remove('checked'));
    });

    document.getElementById('train-ham-selected-button').addEventListener('click', async () => {
        if (selectedUIDs.size === 0 && !selectAllInMailbox) return;
        const count = selectAllInMailbox ? totalMessages : selectedUIDs.size;

        if (!await showConfirm(`Train ${count} email(s) as not spam?`, 'Train')) return;

        const payload = { account: accountName, mailbox: mailboxName, spam: false };
        if (selectAllInMailbox) { payload.all = true; } else { payload.uids = [...selectedUIDs].map(Number); }
        const res = await fetch('/api/spam/train-batch', {
            method: 'POST', credentials: 'include',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload),
        });
        checkResponse(res);
        if (res.ok) {
            const data = await res.json();
            showToast(`Trained ${data.trained} email(s) as not spam`, 'success');
        }
        selectedUIDs.clear();
        selectAllInMailbox = false;
        updateSelectionUI();
        document.getElementById('select-all-checkbox').checked = false;
        document.querySelectorAll('.mail-select-checkbox').forEach(cb => { cb.checked = false; });
        document.querySelectorAll('.mail-row.checked').forEach(r => r.classList.remove('checked'));
    });

    // Move selected: populate mailbox list and handle selection
    const moveSel = document.getElementById('move-selected-select');
    if (moveSel) {
        const populateMoveSelect = async () => {
            moveSel.innerHTML = '';
            moveSel.appendChild(new Option('Move to...', ''));
            try {
                let mailboxData = await fetchMailboxes(accountName);
                let mailboxes = mailboxData.mailboxes;
                mailboxes.sort((a, b) => a.localeCompare(b));
                mailboxes.filter(mb => mb !== mailboxName).forEach(mb => moveSel.appendChild(new Option(mb, mb)));
            } catch (e) { /* ignore */ }
        };
        populateMoveSelect();

        moveSel.addEventListener('change', async () => {
            const toMailbox = moveSel.value;
            if (!toMailbox || (selectedUIDs.size === 0 && !selectAllInMailbox)) { moveSel.value = ''; return; }
            const payload = { account: accountName, mailbox: mailboxName, to_mailbox: toMailbox };
            if (selectAllInMailbox) { payload.all = true; } else { payload.uids = [...selectedUIDs].map(Number); }
            const res = await fetch('/api/mails/move-selected', {
                method: 'POST',
                credentials: 'include',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload),
            });
            checkResponse(res);
            if (!res.ok) {
                showToast('Failed to move selected emails: ' + await res.text(), 'error');
                moveSel.value = '';
                return;
            }
            selectedUIDs.clear();
            renderMails(accountName, mailboxName, null, currentPage);
            refreshSidebarCounts(accountName);
        });
    }

    if (mails === null) {
        const data = await fetchMails(accountName, mailboxName, currentPage);
        mails = data.conversations;
        totalConversations = data.total;
        pageSize = data.pageSize;
        currentPg = data.page;
        currentPage = currentPg;
    }
    if (!mails || mails.length === 0) {
        const scrollDiv = document.getElementById('mail-list-scroll');
        const noMailsDiv = document.createElement('div');
        noMailsDiv.className = 'no-mail-message';
        noMailsDiv.textContent = showUnreadOnly ? "No unread mails" : "No mails";
        scrollDiv.appendChild(noMailsDiv);
        return;
    }

    // Build Gmail-style mail rows
    const mailListDiv = document.getElementById('mail-list-scroll');

    const threadStates = loadThreadStates();
    const activeThreadKeys = new Set();

    for (const conversation of mails) {
        const isThread = conversation.emails.length > 1;
        const threadId = isThread ? 'thread-' + conversation.emails[0].uid : '';
        const threadStateKey = isThread ? getThreadStateKey(accountName, mailboxName, threadId) : '';
        if (isThread) activeThreadKeys.add(threadStateKey);
        const hasUnread = isThread && conversation.emails.some(m => !(m.flags && m.flags.includes('\\Seen')));
        // Default: collapsed. Expand if user explicitly opened it OR thread has unread emails.
        const isCollapsed = isThread && !threadStates[threadStateKey] && !hasUnread;

        conversation.emails.forEach((mail, index) => {
            const from = mail.from && mail.from.length > 0 ? mail.from[0].PersonalName || mail.from[0].MailboxName : '';
            const date = new Date(mail.date);
            const today = new Date();
            const isToday = date.toDateString() === today.toDateString();
            const timeString = isToday ? date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : `${date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })}`;
            const isRead = mail.flags && mail.flags.includes('\\Seen');

            let subject = mail.subject || '[No Subject]';
            let hasAttachment = mail.attachments && mail.attachments.length > 0;

            const row = document.createElement('div');
            row.className = `mail-row ${isRead ? 'read' : 'unread'}`;
            row.tabIndex = 0;
            row.dataset.account = accountName;
            row.dataset.mailbox = mailboxName;
            row.dataset.uid = mail.uid;

            if (isThread) row.dataset.thread = threadId;
            if (isThread && index > 0) {
                row.classList.add(`thread-child-${threadId}`, 'thread-child');
                if (isCollapsed) row.style.display = 'none';
            }

            // Selection checkbox
            const checkbox = document.createElement('input');
            checkbox.type = 'checkbox';
            checkbox.className = 'mail-select-checkbox';
            checkbox.dataset.uid = mail.uid;
            checkbox.addEventListener('change', (e) => {
                e.stopPropagation();
                if (checkbox.checked) {
                    selectedUIDs.add(String(mail.uid));
                    row.classList.add('checked');
                } else {
                    selectedUIDs.delete(String(mail.uid));
                    row.classList.remove('checked');
                }
                // Select/deselect all thread siblings
                const tid = row.dataset.thread;
                if (tid) {
                    document.querySelectorAll(`[data-thread="${tid}"]`).forEach(r => {
                        const cb = r.querySelector('.mail-select-checkbox');
                        if (cb && cb !== checkbox) {
                            cb.checked = checkbox.checked;
                            if (checkbox.checked) {
                                selectedUIDs.add(cb.dataset.uid);
                                r.classList.add('checked');
                            } else {
                                selectedUIDs.delete(cb.dataset.uid);
                                r.classList.remove('checked');
                            }
                        }
                    });
                }
                updateSelectionUI();
            });
            checkbox.addEventListener('click', (e) => e.stopPropagation());
            row.appendChild(checkbox);

            // Avatar
            const avatar = document.createElement('div');
            avatar.className = 'mail-avatar';
            avatar.style.backgroundColor = getAvatarColor(from);
            avatar.textContent = getInitials(from);
            row.appendChild(avatar);

            // Sender
            const senderDiv = document.createElement('div');
            senderDiv.className = 'mail-sender';

            if (isThread && index === 0) {
                const toggleBtn = document.createElement('span');
                toggleBtn.style.cssText = 'cursor:pointer; margin-right:6px; font-size:10px; color:var(--secondary-color);';
                toggleBtn.innerHTML = isCollapsed ? '&#9654;' : '&#9660;';
                toggleBtn.onclick = (evt) => toggleThread(evt, threadId);
                senderDiv.appendChild(toggleBtn);
            }

            const senderText = document.createTextNode(from);
            senderDiv.appendChild(senderText);

            if (isThread && index === 0) {
                const countSpan = document.createElement('span');
                countSpan.className = 'mail-thread-count';
                countSpan.textContent = `(${conversation.emails.length})`;
                senderDiv.appendChild(countSpan);
            }

            row.appendChild(senderDiv);

            // Subject area
            const subjectArea = document.createElement('div');
            subjectArea.className = 'mail-subject-area';

            const subjectText = document.createElement('span');
            subjectText.className = 'mail-subject-text';
            subjectText.textContent = subject;
            subjectArea.appendChild(subjectText);

            appendOrderedChips(subjectArea, mail.labels, mail.flags);

            row.appendChild(subjectArea);

            // Attachment icon
            if (hasAttachment) {
                const attachIcon = document.createElement('span');
                attachIcon.className = 'mail-attachment-icon';
                attachIcon.innerHTML = svgAttachmentIcon(16);
                row.appendChild(attachIcon);
            }

            // Spam score placeholder (filled async if toggle is on)
            if (showSpamScores) {
                const spamDiv = document.createElement('div');
                spamDiv.className = 'mail-spam-score';
                spamDiv.dataset.uid = mail.uid;
                row.appendChild(spamDiv);
            }

            // Date
            const dateDiv = document.createElement('div');
            dateDiv.className = 'mail-date';
            dateDiv.textContent = timeString;
            row.appendChild(dateDiv);

            mailListDiv.appendChild(row);
        });
    }

    // Fetch spam scores in batch if toggle is on
    if (showSpamScores && mails && mails.length > 0) {
        const allUIDs = [];
        for (const conv of mails) {
            for (const m of conv.emails) {
                allUIDs.push(m.uid);
            }
        }
        fetch('/api/spam/classify-batch', {
            method: 'POST', credentials: 'include',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ account: accountName, mailbox: mailboxName, uids: allUIDs })
        })
        .then(r => r.ok ? r.json() : null)
        .then(data => {
            if (!data) return;
            const allScoreDivs = mailListDiv.querySelectorAll('.mail-spam-score');
            if (!data.trained) {
                allScoreDivs.forEach(el => {
                    el.textContent = '\u2014';
                    el.title = 'Not trained yet \u2014 mark some emails as spam and not-spam first';
                    el.style.color = 'var(--secondary-color)';
                });
                return;
            }
            for (const [uid, score] of Object.entries(data.scores)) {
                const el = mailListDiv.querySelector(`.mail-spam-score[data-uid="${uid}"]`);
                if (!el) continue;
                const pct = Math.round(score * 100);
                el.textContent = pct + '%';
                el.title = 'Spam probability: ' + pct + '%';
                if (score >= 0.8) {
                    el.style.color = '#d93025';
                    el.style.fontWeight = '600';
                } else if (score >= 0.5) {
                    el.style.color = '#f9ab00';
                } else {
                    el.style.color = '#188038';
                }
            }
        })
        .catch(() => {});
    }

    // Populate pagination controls in both top and bottom bars
    function fillPaginationBar(container, includeSizeSelect) {
        if (!container || totalConversations <= 0) return;
        const totalPages = Math.ceil(totalConversations / pageSize);

        if (includeSizeSelect) {
            const sizeSelect = document.createElement('select');
            sizeSelect.className = 'pagination-size-select';
            sizeSelect.title = 'Rows per page';
            const autoSize = calcAutoPageSize();
            const options = [
                { label: `Auto (${autoSize})`, value: 0 },
                { label: '25', value: 25 },
                { label: '50', value: 50 },
                { label: '100', value: 100 },
                { label: '200', value: 200 },
            ];
            for (const opt of options) {
                const o = document.createElement('option');
                o.value = opt.value;
                o.textContent = opt.label;
                if (opt.value === PAGE_SIZE) o.selected = true;
                sizeSelect.appendChild(o);
            }
            sizeSelect.onchange = () => {
                PAGE_SIZE = parseInt(sizeSelect.value);
                localStorage.setItem('go-mail-page-size', PAGE_SIZE);
                currentPage = 1;
                renderMails(accountName, mailboxName, null, 1);
            };
            container.appendChild(sizeSelect);
        }

        if (totalPages > 1) {
            const start = (currentPg - 1) * pageSize + 1;
            const end = Math.min(currentPg * pageSize, totalConversations);
            const infoSpan = document.createElement('span');
            infoSpan.className = 'pagination-info';
            infoSpan.textContent = `${start}\u2013${end} of ${totalConversations}`;
            container.appendChild(infoSpan);

            const prevBtn = document.createElement('button');
            prevBtn.className = 'btn-icon pagination-btn';
            prevBtn.disabled = currentPg <= 1;
            prevBtn.innerHTML = '<svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M15.41 7.41L14 6l-6 6 6 6 1.41-1.41L10.83 12z"/></svg>';
            prevBtn.onclick = () => { currentPage = currentPg - 1; renderMails(accountName, mailboxName, null, currentPage); };
            container.appendChild(prevBtn);

            const nextBtn = document.createElement('button');
            nextBtn.className = 'btn-icon pagination-btn';
            nextBtn.disabled = currentPg >= totalPages;
            nextBtn.innerHTML = '<svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M10 6L8.59 7.41 13.17 12l-4.58 4.59L10 18l6-6z"/></svg>';
            nextBtn.onclick = () => { currentPage = currentPg + 1; renderMails(accountName, mailboxName, null, currentPage); };
            container.appendChild(nextBtn);
        } else {
            const infoSpan = document.createElement('span');
            infoSpan.className = 'pagination-info';
            infoSpan.textContent = `${totalConversations} conversations`;
            container.appendChild(infoSpan);
        }
    }

    // Clean up stale thread states for this mailbox
    const prefix = `${accountName}|${mailboxName}|`;
    let cleaned = false;
    for (const key in threadStates) {
        if (key.startsWith(prefix) && !activeThreadKeys.has(key)) {
            delete threadStates[key];
            cleaned = true;
        }
    }
    if (cleaned) saveThreadStates(threadStates);

    fillPaginationBar(document.getElementById('pagination-controls'), true);
    fillPaginationBar(document.getElementById('pagination-bottom'), false);

    // Restore selection after re-render (e.g. during polling refresh)
    if (prevSelected && prevSelected.size > 0) {
        selectAllInMailbox = prevSelectAll;
        const allCheckboxes = document.querySelectorAll('.mail-select-checkbox:not(#select-all-checkbox)');
        allCheckboxes.forEach(cb => {
            if (prevSelected.has(cb.dataset.uid)) {
                cb.checked = true;
                selectedUIDs.add(cb.dataset.uid);
                const row = cb.closest('.mail-row');
                if (row) row.classList.add('checked');
            }
        });
        updateSelectionUI();
        // Restore select-all banner if it was active
        if (prevSelectAll) {
            const banner = document.getElementById('select-all-banner');
            if (banner) {
                banner.innerHTML = `All ${totalMessages} emails in ${mailboxName} are selected. <a href="#" id="clear-all-mailbox-link">Clear selection</a>`;
                banner.style.display = 'block';
                document.getElementById('clear-all-mailbox-link').addEventListener('click', (evt) => {
                    evt.preventDefault();
                    selectAllInMailbox = false;
                    banner.style.display = 'none';
                    document.getElementById('select-all-checkbox').checked = false;
                    document.getElementById('select-all-checkbox').dispatchEvent(new Event('change'));
                });
            }
        }
    }
}

async function showMail(accountName, mailboxName, uid, e) {
    const mailContent = await fetchMail(accountName, mailboxName, uid);
    const mailHeaderDiv = document.getElementById('mail-header');
    const mailBodyDiv = document.getElementById('mail-body');

    // Build Gmail-style email header
    const senderName = mailContent.from && mailContent.from.length > 0
        ? (mailContent.from[0].PersonalName || mailContent.from[0].MailboxName)
        : 'Unknown';
    const senderEmail = mailContent.from && mailContent.from.length > 0
        ? `${mailContent.from[0].MailboxName}@${mailContent.from[0].HostName}`
        : '';
    const avatarColor = getAvatarColor(senderName);
    const initials = getInitials(senderName);

    let subject = mailContent.subject || '[No Subject]';

    // Date formatting
    const emailDate = new Date(mailContent.date);
    const dateStr = emailDate.toLocaleString(undefined, {
        weekday: 'short', year: 'numeric', month: 'short', day: 'numeric',
        hour: '2-digit', minute: '2-digit'
    });

    // Recipients
    let recipientParts = [];
    if (mailContent.to) recipientParts.push('to ' + formatMailAddressList(mailContent.to, formatMailAddressShort));
    if (mailContent.cc && mailContent.cc.length > 0) recipientParts.push('cc: ' + formatMailAddressList(mailContent.cc, formatMailAddressShort));
    if (mailContent.bcc && mailContent.bcc.length > 0) recipientParts.push('bcc: ' + formatMailAddressList(mailContent.bcc, formatMailAddressShort));

    let labelChipsHTML = '<div id="email-labels" style="margin:4px 0 8px 0;">';
    orderedChipsFor(mailContent.labels, mailContent.flags).forEach(item => {
        const safe = DOMPurify.sanitize(item.name);
        if (item.kind === 'orphan') {
            labelChipsHTML += `<span class="flag-chip flag-chip-removable" data-label="${safe}" title="IMAP flag">${safe}<span class="label-chip-x">&times;</span></span>`;
        } else {
            const color = (item.def && item.def.color) || '#888';
            const title = item.kind === 'imap' ? ' title="IMAP keyword (defined label)"' : '';
            labelChipsHTML += `<span class="label-chip label-chip-removable" style="background-color:${color};" data-label="${safe}"${title}>${safe}<span class="label-chip-x">&times;</span></span>`;
        }
    });
    labelChipsHTML += '</div>';
    let headerHTML = `
        <div class="email-subject-bar">${DOMPurify.sanitize(subject)}</div>
        ${labelChipsHTML}
        <div class="email-header">
            <div class="email-header-avatar" style="background-color:${avatarColor}">${initials}</div>
            <div class="email-header-info">
                <div class="email-header-sender">${DOMPurify.sanitize(senderName)} <span class="email-header-email">&lt;${DOMPurify.sanitize(senderEmail)}&gt;</span></div>
                <div class="email-header-recipients">${DOMPurify.sanitize(recipientParts.join(', '))}</div>
                ${mailContent.replyTo && mailContent.replyTo.length > 0 ? `<div class="email-header-recipients">reply-to: ${DOMPurify.sanitize(formatMailAddressList(mailContent.replyTo, formatMailAddressParen))}</div>` : ''}
            </div>
            <div class="email-header-date">${dateStr}</div>
        </div>
    `;

    // Attachments as chips
    if (mailContent.attachments && mailContent.attachments.length > 0) {
        headerHTML += '<div class="email-attachments">';
        mailContent.attachments.forEach(att => {
            headerHTML += `<a class="email-attachment-chip" href="/api/attachment?account=${accountName}&mailbox=${mailboxName}&uid=${uid}&attachment=${att}" download>
                ${svgAttachmentIcon(16)}
                ${DOMPurify.sanitize(att)}
            </a>`;
        });
        headerHTML += '</div>';
    }

    mailHeaderDiv.innerHTML = headerHTML;

    if (mailContent.contentType === 'text/calendar') {
        try {
            const cal = mailContent.body || '';

            let icalBlock = '';
            let noteText = cal;
            const vcalMatch = cal.match(/BEGIN:VCALENDAR([\s\S]*?)END:VCALENDAR/i);

            if (vcalMatch) {
                icalBlock = vcalMatch[0];
                noteText = cal.replace(icalBlock, '').trim();
            } else {
                const veventOnlyMatch = cal.match(/BEGIN:VEVENT([\s\S]*?)END:VEVENT/i);
                if (veventOnlyMatch) {
                    icalBlock = veventOnlyMatch[0];
                    noteText = cal.replace(icalBlock, '').trim();
                } else {
                    icalBlock = cal;
                    noteText = '';
                }
            }

            const unfolded = icalBlock.replace(/\r\n[ \t]/g, '');

            const veventMatch = unfolded.match(/BEGIN:VEVENT([\s\S]*?)END:VEVENT/i);
            const block = veventMatch ? veventMatch[1] : unfolded;

            const getProp = (name) => {
                const re = new RegExp('^' + name + '(?:;[^:]*)?:(.*)$', 'im');
                const m = block.match(re);
                return m ? m[1].trim() : '';
            };

            const getParam = (lineName, paramName) => {
                const re = new RegExp('^' + lineName + ';([^:]*):', 'im');
                const m = block.match(re);
                if (!m) return '';
                const params = m[1];
                const pRe = new RegExp(paramName + '=([^;:]+)', 'i');
                const pm = params.match(pRe);
                return pm ? pm[1] : '';
            };

            const summary = getProp('SUMMARY');
            const location = getProp('LOCATION');
            const dtstart = getProp('DTSTART');
            const dtend = getProp('DTEND');
            const dtstartTZID = getParam('DTSTART','TZID');
            const dtendTZID = getParam('DTEND','TZID');
            let description = getProp('DESCRIPTION');

            const orgMatch = block.match(/^ORGANIZER(?:;([^:]*))?:(.*)$/im);
            let organizer = '';
            if (orgMatch) {
                const params = orgMatch[1] || '';
                const val = orgMatch[2] || '';
                const cn = (params.match(/CN=([^;]+)/i) || [])[1];
                organizer = (cn ? cn + ' ' : '') + (val.replace(/^mailto:/i, ''));
            }

            const attendees = [];
            const attendeeRe = /^ATTENDEE(?:;([^:]*))?:(.*)$/gim;
            let aMatch;
            while ((aMatch = attendeeRe.exec(block)) !== null) {
                const aParams = aMatch[1] || '';
                const aVal = aMatch[2] || '';
                const aCN = (aParams.match(/CN=([^;]+)/i) || [])[1];
                const email = aVal.replace(/^mailto:/i, '');
                attendees.push({name: aCN || email, email});
            }

            const meetProp = (block.match(/^X-GOOGLE-CONFERENCE:(.*)$/im) || [])[1] || '';
            const meetFromDesc = (description.match(/https?:\/\/meet\.google\.com\/[a-z0-9\-]+/i) || [])[0] || '';
            const meetUrl = meetFromDesc || meetProp || (location && location.startsWith('https://meet.google.com') ? location : '');

            const decodeHTMLEntities = (str) => {
                const txt = document.createElement('textarea');
                txt.innerHTML = str || '';
                return txt.value;
            };

            description = decodeHTMLEntities(description || '');
            description = description.replace(/\\n/g, '\n');
            const descriptionHtml = DOMPurify.sanitize(description.replace(/(\r\n|\n|\r)/gm, '<br>'), {ALLOWED_TAGS: ['a','br','b','i','strong','em','ul','li','p'], ALLOWED_ATTR: ['href','target']});

            const formatICalDate = (raw, tzid) => {
                if (!raw) return '';
                const m = raw.match(/^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})(Z)?$/);
                if (!m) return DOMPurify.sanitize(raw);
                const iso = `${m[1]}-${m[2]}-${m[3]}T${m[4]}:${m[5]}:${m[6]}${m[7] ? 'Z' : ''}`;
                if (m[7]) {
                    const d = new Date(iso);
                    return d.toLocaleString(undefined, {weekday: 'long', year: 'numeric', month: 'long', day: 'numeric'}) + ' ' + d.toLocaleTimeString([], {hour: '2-digit', minute: '2-digit'});
                }
                const friendly = new Date(`${m[1]}-${m[2]}-${m[3]}T${m[4]}:${m[5]}:${m[6]}`);
                const datePart = friendly.toLocaleString(undefined, {weekday: 'long', year: 'numeric', month: 'long', day: 'numeric'});
                const timePart = `${m[4]}:${m[5]}`;
                return `${datePart} ${timePart} ${tzid ? '(' + tzid + ')' : ''}`;
            };

            const whenText = (dtstart ? formatICalDate(dtstart, dtstartTZID) : '') + (dtend ? ' - ' + formatICalDate(dtend, dtendTZID) : '');

            let out = '';
            if (noteText) {
                out += `<div style="margin-bottom:12px; white-space: pre-wrap; font-family: sans-serif;">${DOMPurify.sanitize(noteText)}</div>`;
            }
            out += '<div style="border:1px solid var(--border-color); padding:16px; background:var(--white); max-width:900px; border-radius:8px;">';
            if (meetUrl) {
                out += `<div style="margin-bottom:12px;"><a href="${DOMPurify.sanitize(meetUrl)}" target="_blank" style="display:inline-block;background:var(--primary-color);color:var(--contrast-text);padding:10px 20px;border-radius:20px;text-decoration:none;font-weight:500;">Join with Google Meet</a></div>`;
            }
            const icsName = (mailContent.attachments || []).find(a => a.toLowerCase().endsWith('.ics')) || '';
            if (icsName) {
                const icsUrl = `/api/attachment?account=${encodeURIComponent(accountName)}&mailbox=${encodeURIComponent(mailboxName)}&uid=${encodeURIComponent(uid)}&attachment=${encodeURIComponent(icsName)}`;
                out += `<div style="margin-bottom:8px;"><a href="${icsUrl}" download style="margin-right:8px;">Add to calendar</a>`;
                out += ` <span style="color:var(--secondary-color); font-size:0.9em;">(${DOMPurify.sanitize(icsName)})</span></div>`;
            }
            out += `<div style="margin-bottom:12px; display:flex; gap:8px;">
                        <button id="rsvp-accept-${uid}" class="btn btn-success" style="padding:6px 20px;">Accept</button>
                        <button id="rsvp-tentative-${uid}" class="btn btn-warning" style="padding:6px 20px;">Tentative</button>
                        <button id="rsvp-decline-${uid}" class="btn btn-danger" style="padding:6px 20px;">Decline</button>
                    </div>`;
            out += '<table style="width:100%;"><tbody>';
            if (summary) out += `<tr><td style="font-weight:500;padding:6px 12px 6px 0;width:100px;color:var(--secondary-color);vertical-align:top;">Title</td><td style="padding:6px 0;">${DOMPurify.sanitize(summary)}</td></tr>`;
            if (location) out += `<tr><td style="font-weight:500;padding:6px 12px 6px 0;color:var(--secondary-color);vertical-align:top;">Location</td><td style="padding:6px 0;">${DOMPurify.sanitize(location || '')}</td></tr>`;
            if (whenText) out += `<tr><td style="font-weight:500;padding:6px 12px 6px 0;color:var(--secondary-color);vertical-align:top;">When</td><td style="padding:6px 0;">${DOMPurify.sanitize(whenText)}</td></tr>`;
            if (organizer) out += `<tr><td style="font-weight:500;padding:6px 12px 6px 0;color:var(--secondary-color);vertical-align:top;">Organizer</td><td style="padding:6px 0;">${DOMPurify.sanitize(organizer)}</td></tr>`;
            if (descriptionHtml) out += `<tr><td style="vertical-align:top;font-weight:500;padding:6px 12px 6px 0;color:var(--secondary-color);">Description</td><td style="padding:6px 0;">${descriptionHtml}</td></tr>`;
            if (attendees && attendees.length > 0) {
                let attHtml = '<div style="display:flex;flex-direction:column;gap:4px;">';
                attendees.forEach(a => {
                    attHtml += `<div>${DOMPurify.sanitize(a.name)} &lt;${DOMPurify.sanitize(a.email)}&gt;</div>`;
                });
                attHtml += '</div>';
                out += `<tr><td style="vertical-align:top;font-weight:500;padding:6px 12px 6px 0;color:var(--secondary-color);">Attendees</td><td style="padding:6px 0;">${attHtml}</td></tr>`;
            }
            out += '</tbody></table>';
            out += '</div>';

            if (out === '<div style="border:1px solid var(--border-color); padding:16px; background:var(--white); max-width:900px; border-radius:8px;"></div>') {
                const safe = DOMPurify.sanitize(mailContent.body || '');
                mailBodyDiv.innerHTML = `<pre style="white-space: pre-wrap;">${safe.replace(/(\r\n|\n|\r)/gm, '\n')}</pre>`;
            } else {
                mailBodyDiv.innerHTML = out;
            }

            const mailQuery = `account=${encodeURIComponent(accountName)}&mailbox=${encodeURIComponent(mailboxName)}&uid=${encodeURIComponent(uid)}`;
            // Google only records an RSVP through its own tokenized link, opened while signed in.
            const googleLinks = {};
            fetch(`/api/mail/rsvp-links?${mailQuery}`, { credentials: 'include' })
                .then(res => (res.ok ? res.json() : {}))
                .then(links => Object.assign(googleLinks, links || {}))
                .catch(() => {});

            const doRsvp = async (resp) => {
                const url = `/api/mail/rsvp?${mailQuery}&response=${encodeURIComponent(resp)}`;
                try {
                    const res = await fetch(url, { method: 'POST', credentials: 'include' });
                    checkResponse(res);
                    if (!res.ok) throw new Error((await res.text()).trim() || ('server returned ' + res.status));
                    const data = await res.json();
                    const viaGoogle = googleLinks[resp] ? ', opened Google Calendar to record it' : '';
                    showToast('RSVP ' + resp.toLowerCase() + ' sent to ' + data.organizer + viaGoogle, 'success');
                } catch (err) {
                    showToast('RSVP failed: ' + err.message, 'error');
                }
            };
            // window.open must run synchronously in the click handler or popup blockers eat it.
            const respond = (resp) => {
                if (googleLinks[resp]) window.open(googleLinks[resp], '_blank', 'noopener');
                doRsvp(resp);
            };

            const acceptBtn = document.getElementById(`rsvp-accept-${uid}`);
            const tentBtn = document.getElementById(`rsvp-tentative-${uid}`);
            const declineBtn = document.getElementById(`rsvp-decline-${uid}`);
            if (acceptBtn) acceptBtn.addEventListener('click', () => respond('ACCEPTED'));
            if (tentBtn) tentBtn.addEventListener('click', () => respond('TENTATIVE'));
            if (declineBtn) declineBtn.addEventListener('click', () => respond('DECLINED'));
        } catch (err) {
            const safe = DOMPurify.sanitize(mailContent.body || '');
            mailBodyDiv.innerHTML = `<pre style="white-space: pre-wrap;">${safe.replace(/(\r\n|\n|\r)/gm, '\n')}</pre>`;
        }
    } else if (mailContent.contentType === 'text/plain') {
        // Only use plain text rendering as a fallback; escape first so a
        // sender cannot inject markup/script via a plain-text body.
        mailBodyDiv.innerHTML = escapeHtml(mailContent.body).replace(/(\r\n|\n|\r)/gm, "<br>");
    } else {
        // Default: render as HTML (sanitized)
        mailBodyDiv.innerHTML = DOMPurify.sanitize(mailContent.body, {
            ALLOW_DATA_ATTR: false,
            ADD_ATTR: ['style', 'bgcolor', 'color', 'background', 'align', 'valign', 'width', 'height', 'cellpadding', 'cellspacing', 'border'],
        });
    }
    document.getElementById('mail-content').dataset.uid = uid;
    document.getElementById('mail-content').dataset.account = accountName;
    document.getElementById('mail-content').dataset.mailbox = mailboxName;
    setNormalizedHash(accountName, mailboxName, uid);
    openTab(e, 'mail-content', 'mail-tabcontent', 'mail-tablinks');

    document.getElementById('reply-button').onclick = () => replyToMail(mailContent, false, { account: accountName, mailbox: mailboxName });
    document.getElementById('reply-all-button').onclick = () => replyToMail(mailContent, true, { account: accountName, mailbox: mailboxName });

    // Label add/remove handlers
    document.getElementById('add-label-button').onclick = async () => {
        await loadLabelsData();
        const list = document.getElementById('label-picker-list');
        list.innerHTML = '';
        const currentLabels = (mailContent.labels || []).map(l => l.toLowerCase());
        if (labelsData.length === 0) {
            const li = document.createElement('li');
            li.textContent = 'No labels defined. Create one in the Labels tab.';
            li.style.cursor = 'default';
            list.appendChild(li);
        } else {
            sortedLabelDefs(labelsData).forEach(lbl => {
                const li = document.createElement('li');
                li.style.display = 'flex';
                li.style.alignItems = 'center';
                li.style.gap = '8px';
                const dot = document.createElement('span');
                dot.style.cssText = 'width:12px;height:12px;border-radius:50%;flex-shrink:0;background-color:' + (lbl.color || '#888');
                li.appendChild(dot);
                const nameSpan = document.createElement('span');
                nameSpan.textContent = lbl.name;
                li.appendChild(nameSpan);
                if (currentLabels.includes(lbl.name.toLowerCase())) {
                    li.style.opacity = '0.5';
                    nameSpan.textContent += ' (already applied)';
                } else {
                    li.onclick = async () => {
                        document.getElementById('label-picker-overlay').style.display = 'none';
                        document.getElementById('label-picker-modal').style.display = 'none';
                        const res = await fetch('/api/mail/label', { method: 'POST', credentials: 'include', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ account: accountName, mailbox: mailboxName, uid: parseInt(uid), label: lbl.name, action: 'add' }) });
                        if (res.ok) {
                            const data = await res.json();
                            mailContent.labels = data.labels;
                            if (data.flags) mailContent.flags = data.flags;
                            const container = document.getElementById('email-labels');
                            if (container) {
                                container.innerHTML = '';
                                orderedChipsFor(data.labels, data.flags || mailContent.flags).forEach(item => {
                                    const chip = document.createElement('span');
                                    if (item.kind === 'orphan') {
                                        chip.className = 'flag-chip flag-chip-removable';
                                        chip.title = 'IMAP flag';
                                    } else {
                                        chip.className = 'label-chip label-chip-removable';
                                        chip.style.backgroundColor = (item.def && item.def.color) || '#888';
                                        if (item.kind === 'imap') chip.title = 'IMAP keyword (defined label)';
                                    }
                                    chip.dataset.label = item.name;
                                    chip.innerHTML = DOMPurify.sanitize(item.name) + '<span class="label-chip-x">&times;</span>';
                                    chip.onclick = () => removeEmailLabel(accountName, mailboxName, uid, item.name, mailContent);
                                    container.appendChild(chip);
                                });
                            }
                        }
                    };
                }
                list.appendChild(li);
            });
        }
        document.getElementById('label-picker-overlay').style.display = 'block';
        document.getElementById('label-picker-modal').style.display = 'block';
    };

    // Wire up label chip remove handlers (covers both local labels and IMAP flags).
    document.querySelectorAll('#email-labels .label-chip-removable, #email-labels .flag-chip-removable').forEach(chip => {
        chip.onclick = () => removeEmailLabel(accountName, mailboxName, uid, chip.dataset.label, mailContent);
    });

    // Update spam button based on current mailbox
    const spamBtn = document.getElementById('spam-button');
    const isJunkMailbox = mailboxName.toLowerCase() === spamMailboxName.toLowerCase();
    if (isJunkMailbox) {
        spamBtn.title = 'Not spam';
        spamBtn.setAttribute('aria-label', 'Not spam');
        spamBtn.innerHTML = '<svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-2 15l-5-5 1.41-1.41L10 14.17l7.59-7.59L19 8l-9 9z"/></svg>';
    } else {
        spamBtn.title = 'Report spam';
        spamBtn.setAttribute('aria-label', 'Report spam');
        spamBtn.innerHTML = '<svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor"><path d="M1 21h22L12 2 1 21zm12-3h-2v-2h2v2zm0-4h-2v-4h2v4z"/></svg>';
    }

    // Fetch and show spam score
    const scoreBadge = document.getElementById('spam-score-badge');
    const notSpamBtn = document.getElementById('not-spam-button');
    scoreBadge.style.display = 'none';
    notSpamBtn.style.display = 'none';
    fetch(`/api/spam/classify?account=${encodeURIComponent(accountName)}&mailbox=${encodeURIComponent(mailboxName)}&uid=${uid}`, { credentials: 'include' })
        .then(r => r.ok ? r.json() : null)
        .then(data => {
            if (!data) return;
            if (!data.trained) {
                scoreBadge.textContent = 'spam: not trained';
                scoreBadge.style.display = 'inline-block';
                scoreBadge.style.background = 'var(--hover-bg)';
                scoreBadge.style.color = 'var(--secondary-color)';
                scoreBadge.title = 'Mark some emails as spam and not-spam to train the filter';
                return;
            }
            const pct = Math.round(data.score * 100);
            scoreBadge.textContent = pct + '% spam';
            scoreBadge.style.display = 'inline-block';
            if (data.score >= 0.8) {
                scoreBadge.style.background = '#d93025';
                scoreBadge.style.color = '#fff';
            } else if (data.score >= 0.5) {
                scoreBadge.style.background = '#f9ab00';
                scoreBadge.style.color = '#000';
            } else {
                scoreBadge.style.background = '#188038';
                scoreBadge.style.color = '#fff';
            }
            // Show "Not spam" button if score > 0% and not already in Junk
            if (pct > 0 && !isJunkMailbox) {
                notSpamBtn.style.display = 'inline-flex';
            }
        })
        .catch(() => {});

    document.getElementById('show-headers-button').onclick = () => {
        const headersList = document.getElementById('headers-list');
        headersList.innerHTML = '';

        function makeHeaderRow(key, value) {
            const row = document.createElement('div');
            row.className = 'header-row';

            const keySpan = document.createElement('span');
            keySpan.className = 'header-key';
            keySpan.textContent = key;
            row.appendChild(keySpan);

            const valSpan = document.createElement('span');
            valSpan.className = 'header-value';
            valSpan.textContent = value;
            row.appendChild(valSpan);

            const btnGroup = document.createElement('span');
            btnGroup.className = 'header-btn-group';

            const filterBtn = document.createElement('button');
            filterBtn.className = 'btn-icon header-filter-btn';
            filterBtn.title = 'Create new filter from this header';
            filterBtn.setAttribute('aria-label', 'Create new filter from this header');
            filterBtn.innerHTML = '<svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor"><path d="M10 18h4v-2h-4v2zM3 6v2h18V6H3zm3 7h12v-2H6v2z"/></svg>';
            filterBtn.onclick = () => {
                document.getElementById('headers-overlay').style.display = 'none';
                document.getElementById('headers-modal').style.display = 'none';
                openNewFilterWithHeader(key, value, accountName, mailboxName);
            };
            btnGroup.appendChild(filterBtn);

            const addBtn = document.createElement('button');
            addBtn.className = 'btn-icon header-filter-btn';
            addBtn.title = 'Add as condition to existing filter';
            addBtn.setAttribute('aria-label', 'Add as condition to existing filter');
            addBtn.innerHTML = '<svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor"><path d="M19 13h-6v6h-2v-6H5v-2h6V5h2v6h6v2z"/></svg>';
            addBtn.onclick = () => {
                showFilterPicker(key, value, accountName);
            };
            btnGroup.appendChild(addBtn);

            row.appendChild(btnGroup);
            return row;
        }

        // Envelope fields at top
        const envelopeFields = [
            { key: 'From', addrs: mailContent.from },
            { key: 'To', addrs: mailContent.to },
            { key: 'Cc', addrs: mailContent.cc },
            { key: 'Reply-To', addrs: mailContent.replyTo },
        ];
        envelopeFields.forEach(({ key, addrs }) => {
            if (addrs && addrs.length > 0) {
                const val = formatMailAddressList(addrs);
                headersList.appendChild(makeHeaderRow(key, val));
                const emailOnly = addrs.map(mailAddressEmail).join(', ');
                if (emailOnly !== val) {
                    headersList.appendChild(makeHeaderRow(key + ':e-mail', emailOnly));
                }
            }
        });

        // Separator
        const headers = mailContent.headers || {};
        const keys = Object.keys(headers).sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase()));
        if (keys.length > 0) {
            const sep = document.createElement('hr');
            sep.className = 'headers-separator';
            headersList.appendChild(sep);
        }

        if (keys.length === 0 && headersList.children.length === 0) {
            headersList.innerHTML = '<div class="no-mail-message">No headers available. Restart server to fetch headers.</div>';
        } else {
            keys.forEach(key => {
                headersList.appendChild(makeHeaderRow(key, headers[key]));
            });
        }
        document.getElementById('headers-overlay').style.display = 'block';
        document.getElementById('headers-modal').style.display = 'block';
    };
}

function logMailDiff(oldConversations, newConversations) {
    oldConversations = oldConversations || [];
    newConversations = newConversations || [];

    const oldMails = new Map();
    oldConversations.forEach(conv => conv.emails.forEach(m => oldMails.set(m.uid, m)));

    const newMails = new Map();
    newConversations.forEach(conv => conv.emails.forEach(m => newMails.set(m.uid, m)));

    let changed = false;

    for (const [uid, mail] of newMails.entries()) {
        if (!oldMails.has(uid)) {
            console.log(`New mail (UID: ${uid}): ${mail.subject}`);
            changed = true;
        } else {
            const oldMail = oldMails.get(uid);
            if (JSON.stringify(mail.flags) !== JSON.stringify(oldMail.flags)) {
                console.log(`Mail flags changed (UID: ${uid}): from ${JSON.stringify(oldMail.flags || [])} to ${JSON.stringify(mail.flags || [])}`);
                changed = true;
            }
            oldMails.delete(uid);
        }
    }

    if (oldMails.size > 0) {
        for (const uid of oldMails.keys()) {
            console.log(`Deleted mail (UID: ${uid})`);
        }
        changed = true;
    }

    if (!changed) {
        console.log("No changes detected in mail list.");
    }
}

// --- Filter editor helpers ---
function renderTagList(containerId, items) {
    const container = document.getElementById(containerId);
    container.innerHTML = '';
    items.forEach((it, idx) => {
        const span = document.createElement('span');
        span.className = 'tag';
        span.innerText = it;
        const rm = document.createElement('button');
        rm.style.cssText = 'background:none; border:none; cursor:pointer; color:var(--secondary-color); margin-left:6px; font-size:14px;';
        rm.title = 'Remove';
        rm.setAttribute('aria-label', 'Remove');
        rm.innerText = '\u00d7';
        rm.addEventListener('click', () => {
            items.splice(idx, 1);
            renderTagList(containerId, items);
        });
        span.appendChild(rm);
        container.appendChild(span);
    });
}

// attachContainerDnd wires drag-over/drop on the container so a dragged
// row inserts before the row currently under the pointer.
function attachContainerDnd(container) {
    if (container._dndAttached) return;
    container._dndAttached = true;
    container.addEventListener('dragover', (e) => {
        e.preventDefault();
        const dragging = container.querySelector('.dragging');
        if (!dragging) return;
        const after = [...container.children].find(child => {
            if (child === dragging) return false;
            const r = child.getBoundingClientRect();
            return e.clientY < r.top + r.height / 2;
        });
        if (after) container.insertBefore(dragging, after);
        else container.appendChild(dragging);
    });
}

async function renderFromHash() {
    const hash = window.location.hash.substring(1);
    if (!hash) {
        if (document.querySelector('.account-tablinks')) {
            document.querySelector('.account-tablinks').click();
        }
        return;
    }

    const params = new URLSearchParams(hash);

    const view = params.get('view');
    if (view) {
        const opener = { filters: 'openFilters', labels: 'openLabels', plugins: 'openPlugins' }[view];
        if (opener) {
            // Pass the matching tab button so it gets highlighted, like a real click.
            const btn = Array.from(document.getElementsByClassName('mail-tablinks'))
                .find(b => (b.getAttribute('onclick') || '').includes(opener));
            const evt = btn ? { currentTarget: btn } : null;
            if (view === 'filters') openFilters(evt);
            else if (view === 'labels') openLabels(evt);
            else if (view === 'plugins') openPlugins(evt);
        }
        return;
    }

    const accountName = params.get('account');
    const mailboxName = params.get('mailbox');
    const uid = params.get('uid');

    if (!accountName) return;

    const accountButton = Array.from(document.querySelectorAll('.account-tablinks')).find(b => b.dataset.account === accountName);
    if (accountButton && !accountButton.classList.contains('active')) {
        if (mailboxName) {
            // Activate account tab without triggering INBOX auto-selection
            openTab({ currentTarget: accountButton }, accountName, 'account-tabcontent', 'account-tablinks');
        } else {
            accountButton.click();
        }
    }

    if (!mailboxName) return;

    const mailboxSpan = document.querySelector(`[data-account="${accountName}"][data-mailbox="${mailboxName}"]`);
    if (mailboxSpan) {
        const currentlySelected = document.querySelector('.account-tabcontent span.selected');
        if (currentlySelected) currentlySelected.classList.remove('selected');
        mailboxSpan.classList.add('selected');
    }
    if (mailboxSpan && !uid) {
        currentMailbox.account = accountName;
        currentMailbox.mailbox = mailboxName;
        currentPage = 1;
        openTab(null, 'mails-container', 'mail-tabcontent', 'mail-tablinks');
        renderMails(accountName, mailboxName);
    }

    if (!uid) {
        const mailContentDiv = document.getElementById('mail-content');
        if (mailContentDiv) {
            mailContentDiv.dataset.uid = '';
            mailContentDiv.dataset.account = '';
            mailContentDiv.dataset.mailbox = '';
            mailContentDiv.style.display = 'none';
        }
        const mailHeader = document.getElementById('mail-header');
        const mailBody = document.getElementById('mail-body');
        if (mailHeader) mailHeader.innerHTML = '';
        if (mailBody) mailBody.innerHTML = '';

        openTab(null, 'mails-container', 'mail-tabcontent', 'mail-tablinks');
        return;
    }

    const mailContentDiv = document.getElementById('mail-content');
    if (mailContentDiv.style.display === 'block') {
        const existingUID = mailContentDiv.dataset.uid;
        if (existingUID === uid) {
            return;
        }
    }

    const mailLink = document.querySelector(`[data-uid="${uid}"]`);
    if (mailLink) {
        mailLink.click();
    } else {
        showMail(accountName, mailboxName, uid, new Event('click'));
    }
}

async function refreshMailState() {
    if (refreshInFlight) { refreshRequested = true; return; }
    refreshInFlight = true;
    const refreshIndicator = document.getElementById('refresh-indicator');
    if (refreshIndicator) refreshIndicator.style.display = 'block';
    try {
    const accounts = await fetchAccounts();
    const accountTabs = document.getElementById('account-tabs');
    const accountButtons = accountTabs.querySelectorAll('.account-tablinks');

    let hasNewMail = false;
    const newEmails = []; // unseen mail first discovered during this refresh
    for (const account of accounts) {
        for (const button of accountButtons) {
            if (button.dataset.account === account.Name) {
                const existingBadge = button.querySelector('.account-unread-badge');
                if (existingBadge) existingBadge.remove();
                if (account.Unseen > 0) {
                    const badge = document.createElement('span');
                    badge.className = 'account-unread-badge';
                    badge.textContent = account.Unseen > 99 ? '99+' : account.Unseen;
                    button.appendChild(badge);
                }
                break;
            }
        }

        const prev = prevUnseenCounts[account.Name] || 0;
        if (account.Unseen > prev) hasNewMail = true;

        // Skip all notifications until first filter run completes
        if (filtersHaveRun) {
            const notifCfg = account.notifications || {};
            const notifAllowed = 'Notification' in window && Notification.permission === 'granted' && notificationsEnabled;
            // Per-email notifications: check for new UIDs
            if (notifCfg.emails && notifAllowed) {
                try {
                    const unseenEmails = await (await fetch(`/api/unseen-summary?account=${encodeURIComponent(account.Name)}`, { credentials: 'include' })).json();
                    if (!notifiedUIDs[account.Name]) notifiedUIDs[account.Name] = new Set();
                    for (const e of (unseenEmails || [])) {
                        const uidKey = e.mailbox + ':' + e.uid;
                        if (!notifiedUIDs[account.Name].has(uidKey)) {
                            notifiedUIDs[account.Name].add(uidKey);
                            newEmails.push({ ...e, account: account.Name, avatar: account.avatar });
                        }
                    }
                } catch(err) { console.warn('Failed to fetch unseen summary:', err); }
            }
            // Count notification: only after initial batch is done
            if (initialNotifsDone && notifCfg.count && notifAllowed && account.Unseen > prev) {
                const newCount = account.Unseen - prev;
                const cn = new Notification('Go Mail', {
                    body: `${account.Name}: ${newCount} new email${newCount > 1 ? 's' : ''}`,
                    icon: account.avatar,
                    tag: 'go-mail-count-' + account.Name,
                });
                cn.onclick = () => {
                    window.focus();
                    window.location.hash = buildCanonicalHash(account.Name, 'INBOX');
                    cn.close();
                };
            }
        }
        prevUnseenCounts[account.Name] = account.Unseen;

        const mailboxData = await fetchMailboxes(account.Name);
        for (const mailbox of mailboxData.mailboxes) {
            const status = await fetchMailboxStatus(account.Name, mailbox);
            const spans = document.querySelectorAll(`span[data-account="${account.Name}"][data-mailbox="${mailbox}"]`);
            spans.forEach(span => updateMailboxCountBadge(span, status));
        }
    }
    // More than 5 new mails in one refresh collapse into a single summary
    if (filtersHaveRun) {
        const wasInitial = !initialNotifsDone;
        initialNotifsDone = true;
        const notifAllowed = 'Notification' in window && Notification.permission === 'granted' && notificationsEnabled;
        if (notifAllowed && newEmails.length > 5) {
            const mailboxMap = {};
            for (const e of newEmails) {
                if (!mailboxMap[e.account]) mailboxMap[e.account] = new Set();
                mailboxMap[e.account].add(e.mailbox);
            }
            const lines = [];
            for (const [acc, mailboxes] of Object.entries(mailboxMap)) {
                lines.push(acc + ': ' + [...mailboxes].join(', '));
            }
            const first = newEmails[0];
            const sn = new Notification('Go Mail \u2014 ' + newEmails.length + (wasInitial ? ' unread emails' : ' new emails'), {
                body: lines.join('\n'),
                icon: Object.keys(mailboxMap).length === 1 ? first.avatar : undefined,
                tag: 'go-mail-summary-' + Date.now(),
            });
            sn.onclick = () => {
                window.focus();
                if (Object.keys(mailboxMap).length === 1) {
                    window.location.hash = buildCanonicalHash(first.account, first.mailbox);
                }
                sn.close();
            };
        } else if (notifAllowed) {
            for (const e of newEmails) {
                const notifBody = e.snippet ? e.subject + '\n' + e.snippet : e.subject;
                const n = new Notification(e.from || e.account, {
                    body: notifBody,
                    icon: e.avatar,
                    tag: 'go-mail-email-' + e.account + '-' + e.mailbox + '-' + e.uid,
                });
                n.onclick = () => {
                    window.focus();
                    window.location.hash = buildCanonicalHash(e.account, e.mailbox, e.uid);
                    n.close();
                };
            }
        }
    }
    if (hasNewMail) playNewMailSound();

    const mailContentDiv = document.getElementById('mail-content');
    if (currentMailbox.account && currentMailbox.mailbox && document.getElementById('mails-container').style.display !== 'none' && (!mailContentDiv || mailContentDiv.style.display !== 'block')) {
        const mailsKey = `${currentMailbox.account}-${currentMailbox.mailbox}`;
        const curStatus = await fetchMailboxStatus(currentMailbox.account, currentMailbox.mailbox);
        const statusStr = JSON.stringify(curStatus);
        if (statusStr !== lastMailboxStatus[mailsKey]) {
            lastMailboxStatus[mailsKey] = statusStr;
            const newData = await fetchMails(currentMailbox.account, currentMailbox.mailbox, currentPage);
            const oldMails = lastMailsData[mailsKey];

            if (JSON.stringify(newData) !== JSON.stringify(oldMails)) {
                console.log(`Mail data for ${mailsKey} has changed.`);
                logMailDiff(oldMails && oldMails.conversations, newData && newData.conversations);
                lastMailsData[mailsKey] = newData;
                renderMails(currentMailbox.account, currentMailbox.mailbox, newData, null, true);
            }
        }
    }

    } catch (err) {
        console.warn('Poll error:', err);
    }
    if (refreshIndicator) refreshIndicator.style.display = 'none';
    refreshInFlight = false;
    if (refreshRequested) { refreshRequested = false; refreshMailState(); }
}
