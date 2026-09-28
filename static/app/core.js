// core: function declarations shared globally with the other app/*.js files.

function getAvatarColor(name) {
    let hash = 0;
    for (let i = 0; i < name.length; i++) {
        hash = name.charCodeAt(i) + ((hash << 5) - hash);
    }
    return AVATAR_COLORS[Math.abs(hash) % AVATAR_COLORS.length];
}

function getInitials(name) {
    if (!name) return '?';
    const parts = name.trim().split(/\s+/);
    if (parts.length >= 2) {
        return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
    }
    return name[0].toUpperCase();
}

function customFlags(flags) {
    if (!flags || !flags.length) return [];
    return flags.filter(f => !SYSTEM_IMAP_FLAGS.has(f));
}

// Unified, weight-sorted chip list for a single email. Merges local
// labels and custom IMAP flags so `weight` controls absolute order
// regardless of storage. Hidden labels are filtered out here.
function orderedChipsFor(labels, flags) {
    const items = [];
    (labels || []).forEach(name => {
        const def = labelsData && labelsData.find(l => l.name === name);
        if (def && def.hidden) return;
        items.push({ name, def, kind: 'local' });
    });
    customFlags(flags || []).forEach(name => {
        const def = labelsData && labelsData.find(l => l.name === name);
        if (def && def.hidden) return;
        items.push({ name, def, kind: def ? 'imap' : 'orphan' });
    });
    const w = it => (it.def && typeof it.def.weight === 'number') ? it.def.weight : 0;
    items.sort((a, b) => {
        if (w(a) !== w(b)) return w(a) - w(b);
        return a.name.localeCompare(b.name);
    });
    return items;
}

function appendOrderedChips(container, labels, flags) {
    orderedChipsFor(labels, flags).forEach(item => {
        const chip = document.createElement('span');
        if (item.kind === 'orphan') {
            chip.className = 'flag-chip';
            chip.title = 'IMAP flag';
        } else {
            chip.className = 'label-chip';
            chip.style.backgroundColor = (item.def && item.def.color) || '#888';
            if (item.kind === 'imap') chip.title = 'IMAP keyword (defined label)';
        }
        chip.textContent = item.name;
        container.appendChild(chip);
    });
}

function checkResponse(response) {
    if (response.status === 401 || response.status === 503) {
        window.location.href = '/login';
        throw new Error('Redirecting to login');
    }
}

function openTab(evt, tabName, contentClass, linksClass) {
    var i, tabcontent, tablinks;
    tabcontent = document.getElementsByClassName(contentClass);
    for (i = 0; i < tabcontent.length; i++) {
        tabcontent[i].style.display = "none";
    }
    tablinks = document.getElementsByClassName(linksClass);
    for (i = 0; i < tablinks.length; i++) {
        tablinks[i].className = tablinks[i].className.replace(" active", "");
    }
    const tabEl = document.getElementById(tabName);
    tabEl.style.display = (tabEl.id === 'mails-container' || tabEl.id === 'search-content') ? 'flex' : 'block';

    const target = evt ? evt.currentTarget : null;
    if (target) {
        target.className += " active";
    } else {
        for (const link of document.getElementsByClassName(linksClass)) {
            if (link.getAttribute('onclick') && link.getAttribute('onclick').includes(`'${tabName}'`)) {
                link.className += " active";
                break;
            }
        }
    }
}

function formatFileSize(bytes) {
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1048576) return (bytes / 1024).toFixed(1) + ' KB';
    return (bytes / 1048576).toFixed(1) + ' MB';
}

function escapeHtml(text) {
    return String(text == null ? '' : text).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

function escapeAttr(text) {
    return escapeHtml(text).replace(/"/g, '&quot;');
}

// Drafts cached before addresses were split carry the whole address in MailboxName.
function mailAddressEmail(addr) {
    return addr.HostName ? `${addr.MailboxName}@${addr.HostName}` : (addr.MailboxName || '');
}

function formatMailAddress(addr) {
    const email = mailAddressEmail(addr);
    return addr.PersonalName ? `${addr.PersonalName} <${email}>` : email;
}

function formatMailAddressShort(addr) {
    return addr.PersonalName || mailAddressEmail(addr);
}

function formatMailAddressParen(addr) {
    const email = mailAddressEmail(addr);
    return addr.PersonalName ? `${addr.PersonalName} (${email})` : email;
}

function formatMailAddressList(addrs, formatter) {
    if (!addrs || addrs.length === 0) return '';
    return addrs.map(formatter || formatMailAddress).join(', ');
}

function svgAttachmentIcon(size) {
    return `<svg width="${size}" height="${size}" viewBox="0 0 24 24" fill="currentColor"><path d="M16.5 6v11.5c0 2.21-1.79 4-4 4s-4-1.79-4-4V5c0-1.38 1.12-2.5 2.5-2.5s2.5 1.12 2.5 2.5v10.5c0 .55-.45 1-1 1s-1-.45-1-1V6H10v9.5c0 1.38 1.12 2.5 2.5 2.5s2.5-1.12 2.5-2.5V5c0-2.21-1.79-4-4-4S7 2.79 7 5v12.5c0 3.04 2.46 5.5 5.5 5.5s5.5-2.46 5.5-5.5V6h-1.5z"/></svg>`;
}

function svgDeleteIcon(size) {
    return `<svg width="${size}" height="${size}" viewBox="0 0 24 24" fill="currentColor"><path d="M6 19c0 1.1.9 2 2 2h8c1.1 0 2-.9 2-2V7H6v12zM19 4h-3.5l-1-1h-5l-1 1H5v2h14V4z"/></svg>`;
}

async function main() {
    await waitForReady();
    const [accounts, mailboxSettings, clientSettings] = await Promise.all([
        fetchAccounts(), fetchMailboxSettings(),
        fetch('/api/settings', { credentials: 'include' }).then(r => r.json()).catch(() => ({})),
    ]);
    // Sound: user localStorage choice wins; otherwise seed from server config.
    const storedSound = localStorage.getItem('go-mail-sound-enabled');
    newMailSoundEnabled = storedSound !== null ? storedSound === 'true' : (clientSettings.new_mail_sound || false);
    updateSoundIcon();
    spamMailboxName = clientSettings.spam_mailbox || 'Junk';
    accountsData = accounts;
    // Initialize unseen counts and known UIDs so first poll doesn't trigger notifications
    for (const acc of accounts) {
        prevUnseenCounts[acc.Name] = acc.Unseen;
    }
    // Pre-populate notifiedUIDs with current unseen emails (no notifications until filters complete)
    try {
        const unseenEmails = await (await fetch('/api/unseen-summary', { credentials: 'include' })).json();
        for (const e of (unseenEmails || [])) {
            if (!notifiedUIDs[e.account]) notifiedUIDs[e.account] = new Set();
            notifiedUIDs[e.account].add(e.mailbox + ':' + e.uid);
        }
    } catch(err) { console.warn('Failed to init unseen UIDs:', err); }
    if (mailboxSettings) {
        mailboxSettings.forEach(s => {
            if (s && s.Mailbox) mailboxSettingsMap[s.Mailbox.toLowerCase()] = s.Icon;
        });
    }
    // Mailbox count toggle
    const countToggleBtn = document.getElementById('mailbox-count-toggle');
    if (countToggleBtn) {
        countToggleBtn.classList.toggle('active', showMailboxCounts);
        countToggleBtn.addEventListener('click', () => {
            showMailboxCounts = !showMailboxCounts;
            localStorage.setItem('go-mail-show-mailbox-counts', showMailboxCounts);
            countToggleBtn.classList.toggle('active', showMailboxCounts);
            refreshAllMailboxCounts();
        });
    }

    const accountTabs = document.getElementById('account-tabs');
    const accountTabContent = document.getElementById('account-tab-content');

    for (const account of accounts) {
        const accountTab = document.createElement('div');
        accountTab.className = 'account-avatar-wrapper account-tablinks';
        accountTab.dataset.account = account.Name;

        const avatarUrl = account.avatar || account.gravatar_url || '';
        const avatar = document.createElement('div');
        avatar.className = 'account-avatar';
        avatar.title = account.Name;

        if (avatarUrl) {
            const img = document.createElement('img');
            img.className = 'account-avatar-img';
            img.src = avatarUrl;
            img.alt = account.Name;
            img.onerror = () => {
                // Fallback to letter avatar on load failure (e.g. no gravatar)
                img.remove();
                avatar.style.backgroundColor = getAvatarColor(account.Name);
                avatar.textContent = getInitials(account.Name);
            };
            avatar.appendChild(img);
        } else {
            avatar.style.backgroundColor = getAvatarColor(account.Name);
            avatar.textContent = getInitials(account.Name);
        }
        accountTab.appendChild(avatar);

        if (account.Unseen > 0) {
            const badge = document.createElement('span');
            badge.className = 'account-unread-badge';
            badge.textContent = account.Unseen > 99 ? '99+' : account.Unseen;
            accountTab.appendChild(badge);
        }

        accountTab.onclick = (event) => {
            openTab(event, account.Name, 'account-tabcontent', 'account-tablinks');
            // Auto-select INBOX, fall back to first mailbox
            const contentDiv = document.getElementById(account.Name);
            if (contentDiv) {
                const inbox = contentDiv.querySelector('span[data-mailbox="INBOX"]');
                const target = inbox || contentDiv.querySelector('span[data-mailbox]');
                if (target) target.click();
            }
        };
        accountTabs.appendChild(accountTab);

        const accountContent = document.createElement('div');
        accountContent.id = account.Name;
        accountContent.className = 'tabcontent account-tabcontent';
        accountTabContent.appendChild(accountContent);

        const mailboxData = await fetchMailboxes(account.Name);
        const mailboxes = mailboxData.mailboxes;
        const mailboxOrder = mailboxData.order;
        accountContent.innerHTML = buildMailboxTree(mailboxes, account.Name, mailboxSettings, mailboxOrder);

        const composeFrom = document.getElementById('compose-from');
        if (composeFrom && account.from_addresses && account.from_addresses.length > 0) {
            const optgroup = document.createElement('optgroup');
            optgroup.label = account.Name;
            account.from_addresses.forEach(f => {
                const option = document.createElement('option');
                const name = f.name || f.Name || '';
                const email = f.email || f.Email || '';
                const display = name ? `${name} <${email}>` : email;
                option.value = display;
                option.text = display;
                optgroup.appendChild(option);
            });
            composeFrom.appendChild(optgroup);
        }

        mailboxes.forEach(mailbox => {
            fetchMailboxStatus(account.Name, mailbox).then(status => {
                const spans = document.querySelectorAll(`span[data-account="${account.Name}"][data-mailbox="${mailbox}"]`);
                spans.forEach(span => updateMailboxCountBadge(span, status));
            });

            const spans = document.querySelectorAll(`span[data-account="${account.Name}"][data-mailbox="${mailbox}"]`);
            spans.forEach(span => {
                span.addEventListener('click', () => {
                    const currentlySelected = document.querySelector('.account-tabcontent span.selected');
                    if (currentlySelected) {
                        currentlySelected.classList.remove('selected');
                    }
                    span.classList.add('selected');
                    setNormalizedHash(account.Name, mailbox, '');
                });
            });
        });
    }
}

// formatGoDuration converts a Go time.Duration JSON value (int64 ns)
// back into the "15s" / "1m" / "2h" form an operator would write. The
// server emits durations as nanoseconds; the textarea should show the
// human form so editing feels natural.
function formatGoDuration(d) {
    if (d == null || d === '') return '';
    if (typeof d === 'string') return d;
    if (typeof d !== 'number') return '';
    if (d === 0) return '';
    if (d % 3600e9 === 0) return (d / 3600e9) + 'h';
    if (d % 60e9 === 0) return (d / 60e9) + 'm';
    if (d % 1e9 === 0) return (d / 1e9) + 's';
    if (d % 1e6 === 0) return (d / 1e6) + 'ms';
    return d + 'ns';
}

// parseDurationToNs reverses formatGoDuration so the operator's
// "15s" lands as the nanosecond int64 Go's time.Duration JSON unmarshal
// expects. Throws on unrecognized syntax — the caller surfaces the
// error as a toast.
function parseDurationToNs(s) {
    s = String(s == null ? '' : s).trim();
    if (s === '') return 0;
    const m = s.match(/^(\d+(?:\.\d+)?)\s*(ns|us|µs|ms|s|m|h)$/);
    if (m) {
        const n = parseFloat(m[1]);
        const mult = {'ns':1, 'us':1e3, 'µs':1e3, 'ms':1e6, 's':1e9, 'm':60e9, 'h':3600e9}[m[2]];
        return Math.round(n * mult);
    }
    if (/^\d+$/.test(s)) return parseInt(s, 10); // raw ns
    throw new Error('invalid duration "' + s + '" — use forms like "15s", "1m", "2h"');
}

// makeDragHandle creates a small drag grip prepended to filter rows so
// users can reorder conditions/actions visually. Reorder semantics:
// backend still runs plugin conditions last regardless of UI order, so
// dragging affects evaluation order only among non-plugin conditions.
function makeDragHandle(wrapper) {
    const grip = document.createElement('span');
    grip.className = 'filter-row-drag';
    grip.draggable = true;
    grip.title = 'Drag to reorder';
    grip.textContent = '⋮⋮';
    grip.style.cursor = 'grab';
    grip.style.padding = '0 6px';
    grip.style.color = 'var(--secondary-color, #888)';
    grip.style.userSelect = 'none';
    grip.addEventListener('dragstart', (e) => {
        wrapper.classList.add('dragging');
        e.dataTransfer.effectAllowed = 'move';
        // Required by Firefox to actually start the drag.
        try { e.dataTransfer.setData('text/plain', ''); } catch (_) {}
    });
    grip.addEventListener('dragend', () => wrapper.classList.remove('dragging'));
    return grip;
}

function makeConditionRow(cond, onRemove) {
    const wrapper = document.createElement('div');
    wrapper.className = 'filter-condition-row';
    wrapper.appendChild(makeDragHandle(wrapper));

    const fieldOptions = [
        { value: 'from', label: 'From' },
        { value: 'sender', label: 'Sender' },
        { value: 'to', label: 'To' },
        { value: 'cc', label: 'Cc' },
        { value: 'bcc', label: 'Bcc' },
        { value: 'from/to', label: 'From / To' },
        { value: 'from/to/cc/bcc', label: 'Any Address' },
        { value: 'subject', label: 'Subject' },
        { value: 'body', label: 'Body' },
        { value: 'age', label: 'Age' },
        { value: 'status', label: 'Status' },
        { value: 'label', label: 'Label' },
        { value: 'header', label: 'Header' },
        { value: 'plugin', label: 'Plugin' },
    ];

    const field = document.createElement('select');
    fieldOptions.forEach(o => field.appendChild(new Option(o.label, o.value)));

    let headerName = '';
    let pluginName = '';
    let labelValue = '';
    let fieldValue = cond.field || 'from';
    if (fieldValue.startsWith('header:')) {
        headerName = fieldValue.substring(7);
        fieldValue = 'header';
    } else if (fieldValue.startsWith('plugin:')) {
        pluginName = fieldValue.substring(7);
        fieldValue = 'plugin';
    } else if (fieldValue === 'label') {
        labelValue = cond.value || '';
    }
    field.value = fieldValue;

    const headerInput = document.createElement('input');
    headerInput.type = 'text';
    headerInput.placeholder = 'Header name...';
    headerInput.value = headerName;
    headerInput.style.maxWidth = '140px';
    headerInput.style.display = fieldValue === 'header' ? '' : 'none';

    const pluginSel = document.createElement('select');
    pluginSel.style.maxWidth = '160px';
    pluginSel.style.display = fieldValue === 'plugin' ? '' : 'none';
    // Populate plugin names lazily; preserve a possibly-not-yet-running plugin name as a stub option.
    const populatePluginSel = async () => {
        pluginSel.innerHTML = '';
        const names = await fetchPluginNames();
        if (pluginName && !names.includes(pluginName)) {
            pluginSel.appendChild(new Option(pluginName + ' (offline)', pluginName));
        }
        if (names.length === 0 && !pluginName) {
            pluginSel.appendChild(new Option('No plugins available', ''));
            pluginSel.disabled = true;
        } else {
            pluginSel.disabled = false;
            names.forEach(n => pluginSel.appendChild(new Option(n, n)));
        }
        if (pluginName) pluginSel.value = pluginName;
    };
    populatePluginSel();

    const labelSel = document.createElement('select');
    labelSel.style.maxWidth = '180px';
    labelSel.style.display = fieldValue === 'label' ? '' : 'none';
    const populateLabelSel = async () => {
        labelSel.innerHTML = '';
        const names = await fetchLabelNames();
        if (labelValue && !names.includes(labelValue)) {
            labelSel.appendChild(new Option(labelValue + ' (missing)', labelValue));
        }
        if (names.length === 0 && !labelValue) {
            labelSel.appendChild(new Option('No labels available', ''));
            labelSel.disabled = true;
        } else {
            labelSel.disabled = false;
            names.forEach(n => labelSel.appendChild(new Option(n, n)));
        }
        if (labelValue) labelSel.value = labelValue;
    };
    populateLabelSel();

    field.addEventListener('change', () => {
        headerInput.style.display = field.value === 'header' ? '' : 'none';
        pluginSel.style.display = field.value === 'plugin' ? '' : 'none';
        labelSel.style.display = field.value === 'label' ? '' : 'none';
        if (field.value === 'plugin') populatePluginSel();
        if (field.value === 'label') populateLabelSel();
        applyFieldUI();
    });

    const opOptions = [
        { value: 'contains', label: 'Contains' },
        { value: 'equals', label: 'Equals' },
        { value: 'not-contains', label: 'Not contains' },
        { value: 'begins_with', label: 'Begins with' },
        { value: 'ends_with', label: 'Ends with' },
        { value: 'matches', label: 'Matches (regex)' },
        { value: 'older_than', label: 'Older than' },
        { value: 'exists', label: 'Exists' },
    ];

    const op = document.createElement('select');
    const populateOps = (allowed) => {
        const prev = op.value;
        op.innerHTML = '';
        opOptions
            .filter(o => !allowed || allowed.includes(o.value))
            .forEach(o => op.appendChild(new Option(o.label, o.value)));
        if (allowed && !allowed.includes(prev)) {
            op.value = allowed[0];
        } else if (prev) {
            op.value = prev;
        }
    };
    populateOps();
    op.value = cond.operator || 'contains';

    const val = document.createElement('input');
    val.type = 'text';
    val.value = cond.value || '';
    val.placeholder = 'Value...';

    // applyFieldUI keeps operator choices and value-input visibility in sync with the
    // selected field. Called on init and on every field change.
    const applyFieldUI = () => {
        if (field.value === 'label') {
            populateOps(['contains', 'not-contains']);
            val.style.display = 'none';
        } else {
            populateOps();
            val.style.display = op.value === 'exists' ? 'none' : '';
        }
    };
    applyFieldUI();

    op.addEventListener('change', () => {
        if (field.value !== 'label') {
            val.style.display = op.value === 'exists' ? 'none' : '';
        }
    });

    const rem = document.createElement('button');
    rem.className = 'filter-row-remove';
    rem.title = 'Remove condition';
    rem.setAttribute('aria-label', 'Remove condition');
    rem.innerHTML = '&times;';
    rem.addEventListener('click', () => { wrapper.remove(); if (onRemove) onRemove(); });

    wrapper.appendChild(field);
    wrapper.appendChild(headerInput);
    wrapper.appendChild(pluginSel);
    wrapper.appendChild(labelSel);
    wrapper.appendChild(op);
    wrapper.appendChild(val);
    wrapper.appendChild(rem);
    wrapper._field = field;
    wrapper._headerInput = headerInput;
    wrapper._pluginSel = pluginSel;
    wrapper._labelSel = labelSel;
    wrapper._op = op;
    wrapper._val = val;
    return wrapper;
}

function makeActionRow(act, onRemove) {
    const wrapper = document.createElement('div');
    wrapper.className = 'filter-action-row';
    wrapper.appendChild(makeDragHandle(wrapper));

    const actionOptions = [
        { value: 'move', label: 'Move to' },
        { value: 'mark_as_read', label: 'Mark as Read' },
        { value: 'mark_as_unread', label: 'Mark as Unread' },
        { value: 'flag', label: 'Add Flag' },
        { value: 'label', label: 'Add Label' },
        { value: 'delete', label: 'Delete' },
        { value: 'permanent_delete', label: 'Permanently Delete' },
        { value: 'plugin_clear_cache', label: 'Plugin: clear cache' },
    ];

    const actSel = document.createElement('select');
    actionOptions.forEach(o => actSel.appendChild(new Option(o.label, o.value)));
    actSel.value = act.type || 'move';

    // Mailbox dropdown for 'move' action
    const mailboxSel = document.createElement('select');
    mailboxSel.style.flex = '1';
    const populateMailboxSelect = async (selectValue) => {
        mailboxSel.innerHTML = '';
        mailboxSel.appendChild(new Option('Select mailbox...', ''));
        // Use the first account in editorState, or first in the dropdown
        const acct = editorState.accounts.length > 0
            ? editorState.accounts[0]
            : document.getElementById('filter-account-input')?.value || '';
        if (acct) {
            try {
                let mailboxData = await fetchMailboxes(acct);
                let mailboxes = mailboxData.mailboxes;
                mailboxes.sort((a, b) => a.localeCompare(b));
                mailboxes.forEach(mb => mailboxSel.appendChild(new Option(mb, mb)));
            } catch (e) { /* ignore */ }
        }
        const sep = new Option('──────────', '__separator__');
        sep.disabled = true;
        mailboxSel.appendChild(sep);
        mailboxSel.appendChild(new Option('+ Create new mailbox...', '__create_new__'));
        const val = selectValue || act.value;
        if (val) mailboxSel.value = val;
    };
    populateMailboxSelect();

    mailboxSel.addEventListener('change', async () => {
        if (mailboxSel.value !== '__create_new__') return;
        const name = prompt('Enter new mailbox name:');
        if (!name || !name.trim()) {
            mailboxSel.value = '';
            return;
        }
        const acct = editorState.accounts.length > 0
            ? editorState.accounts[0]
            : document.getElementById('filter-account-input')?.value || '';
        if (!acct) {
            showToast('Please select an account first.', 'warning');
            mailboxSel.value = '';
            return;
        }
        try {
            const resp = await fetch(`/api/mailbox?account=${encodeURIComponent(acct)}&mailbox=${encodeURIComponent(name.trim())}`, {
                method: 'POST',
                credentials: 'include',
            });
            if (!resp.ok) {
                const err = await resp.text();
                showToast('Failed to create mailbox: ' + err, 'error');
                mailboxSel.value = '';
                return;
            }
            const result = await resp.json();
            await populateMailboxSelect(result.name);
        } catch (e) {
            showToast('Failed to create mailbox: ' + e.message, 'error');
            mailboxSel.value = '';
        }
    });

    // Text input for 'flag' action
    const param = document.createElement('input');
    param.type = 'text';
    param.value = (act.type === 'flag' ? act.value : '') || '';
    param.placeholder = 'Flag name...';

    // Label dropdown for 'label' action
    const labelSel = document.createElement('select');
    labelSel.style.flex = '1';
    labelSel.innerHTML = '';
    labelsData.forEach(l => labelSel.appendChild(new Option(l.name, l.name)));
    if (act.type === 'label' && act.value) labelSel.value = act.value;

    // Plugin dropdown for 'plugin_clear_cache' action; '' = clear all plugins.
    const pluginActionSel = document.createElement('select');
    pluginActionSel.style.flex = '1';
    const populatePluginActionSel = async () => {
        pluginActionSel.innerHTML = '';
        pluginActionSel.appendChild(new Option('All plugins', ''));
        const names = await fetchPluginNames();
        names.forEach(n => pluginActionSel.appendChild(new Option(n, n)));
        const want = act.type === 'plugin_clear_cache' ? (act.value || '') : '';
        if (want && !names.includes(want)) {
            pluginActionSel.appendChild(new Option(want + ' (offline)', want));
        }
        pluginActionSel.value = want;
    };
    populatePluginActionSel();

    const updateValueField = () => {
        const type = actSel.value;
        mailboxSel.style.display = type === 'move' ? '' : 'none';
        param.style.display = type === 'flag' ? '' : 'none';
        labelSel.style.display = type === 'label' ? '' : 'none';
        pluginActionSel.style.display = type === 'plugin_clear_cache' ? '' : 'none';
        if (type === 'plugin_clear_cache') populatePluginActionSel();
    };
    updateValueField();

    actSel.addEventListener('change', updateValueField);

    const rem = document.createElement('button');
    rem.className = 'filter-row-remove';
    rem.title = 'Remove action';
    rem.setAttribute('aria-label', 'Remove action');
    rem.innerHTML = '&times;';
    rem.addEventListener('click', () => { wrapper.remove(); if (onRemove) onRemove(); });

    wrapper.appendChild(actSel);
    wrapper.appendChild(mailboxSel);
    wrapper.appendChild(param);
    wrapper.appendChild(labelSel);
    wrapper.appendChild(pluginActionSel);
    wrapper.appendChild(rem);
    // Store refs for reading
    wrapper._actSel = actSel;
    wrapper._mailboxSel = mailboxSel;
    wrapper._param = param;
    wrapper._labelSel = labelSel;
    wrapper._pluginActionSel = pluginActionSel;
    return wrapper;
}
