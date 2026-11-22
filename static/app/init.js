// Top-level initialization. Loaded last: every other app/*.js file only declares functions.

// Avatar color palette (Gmail-like)
const AVATAR_COLORS = [
    '#1a73e8', '#d93025', '#188038', '#e37400',
    '#a142f4', '#007b83', '#c5221f', '#1967d2',
    '#e8710a', '#137333', '#b31412', '#0d652d',
    '#8430ce', '#0277bd', '#c6401d', '#0b8043'
];

// System IMAP flags that have built-in semantics elsewhere in the UI
// (read/unread state, draft, etc.) — exclude them from the visible-chip set.
const SYSTEM_IMAP_FLAGS = new Set(['\\Seen', '\\Answered', '\\Flagged', '\\Deleted', '\\Draft', '\\Recent']);

let composeMode = 'html'; // 'html' or 'text'

let composeAttachments = []; // array of File objects

let activeDraft = null; // {account, mailbox, uid} when editing a draft

let replyContext = null; // {account, mailbox} of the replied-to mail; copy of the reply is saved there

// CSP forbids inline on* handlers; buttons declare data-action instead
// and this single delegated listener dispatches them. Handlers that
// inspect evt.currentTarget get the declaring element, as before.
document.addEventListener('click', function(e) {
    const el = e.target.closest('[data-action]');
    if (!el) return;
    const evt = {
        currentTarget: el,
        target: e.target,
        preventDefault: () => e.preventDefault(),
        stopPropagation: () => e.stopPropagation(),
    };
    const d = el.dataset;
    switch (d.action) {
        case 'open-compose': openCompose(evt); break;
        case 'open-filters': openFilters(evt); break;
        case 'open-labels': openLabels(evt); break;
        case 'open-plugins': openPlugins(evt); break;
        case 'show-mails': showMails(evt); break;
        case 'open-tab': openTab(evt, d.tab, d.contentClass, d.linksClass); break;
        case 'discard-compose': discardCompose(evt); break;
        case 'close-modal':
            document.getElementById(d.overlay).style.display = 'none';
            document.getElementById(d.modal).style.display = 'none';
            break;
        case 'toast-close': el.parentElement.remove(); break;
        case 'delete-filter': deleteFilterWrapper(evt, d.id); break;
        case 'toggle-filter-enabled': toggleFilterEnabled(evt, d.id); break;
        case 'clear-filter-input': clearFilterInput(d.target); break;
        case 'toggle-filter-card': toggleFilterCard(evt); break;
        case 'delete-label': deleteLabelWrapper(evt, d.name); break;
        case 'plugin-trigger': triggerPluginAction(evt, d.name); break;
        case 'plugin-restart': restartPluginAction(evt, d.name); break;
        case 'plugin-toggle': togglePluginAction(evt, d.name, d.disabled === '1'); break;
        default: return;
    }
    if ('closeMenu' in d) closeSettingsMenu();
});

// --- Filters management ---
let allFilters = [];

// ==================== Labels Management ====================
let labelsData = [];

let labelEditorAccounts = [];

document.addEventListener('DOMContentLoaded', function() {
    const sendBtn = document.getElementById('compose-send');
    if (sendBtn) {
        sendBtn.addEventListener('click', async () => {
            const fromValue = (document.getElementById('compose-from').value || '').trim();
            let name = '';
            let email = fromValue;
            const m = fromValue.match(/^(.*)\s+<(.+)>$/);
            if (m) {
                name = m[1].trim();
                email = m[2].trim();
            }
            const from = { name, email };
            const to = document.getElementById('compose-to').value.split(',').map(s => s.trim()).filter(Boolean);
            const cc = document.getElementById('compose-cc').value.split(',').map(s => s.trim()).filter(Boolean);
            const bcc = document.getElementById('compose-bcc').value.split(',').map(s => s.trim()).filter(Boolean);
            const subject = document.getElementById('compose-subject').value;
            const { body, contentType, inlineImages } = getComposeBody();

            sendBtn.classList.add('sending');
            sendBtn.disabled = true;

            // Resolve sending account from From-address so the Sent copy lands in
            // the owning account's Sent mailbox (not the currently-viewed account/mailbox).
            let sendAccount = currentMailbox.account;
            const fromLower = (email || '').toLowerCase();
            if (fromLower) {
                for (const acc of (accountsData || [])) {
                    const match = (acc.from_addresses || []).some(fa => {
                        const e = (fa.email || fa.Email || '').toLowerCase();
                        return e === fromLower;
                    });
                    if (match) { sendAccount = acc.Name; break; }
                }
            }

            // Save the copy in the replied-to mail's mailbox (server falls back to
            // Sent for special mailboxes). Only when sending from the same account.
            const sendMailbox = (replyContext && replyContext.account === sendAccount) ? replyContext.mailbox : '';

            try {
                let resp;
                if (composeAttachments.length > 0 || inlineImages.length > 0) {
                    const fd = new FormData();
                    fd.append('from_name', name);
                    fd.append('from_email', email);
                    to.forEach(t => fd.append('to', t));
                    cc.forEach(c => fd.append('cc', c));
                    bcc.forEach(b => fd.append('bcc', b));
                    fd.append('subject', subject);
                    fd.append('body', body);
                    fd.append('contentType', contentType);
                    fd.append('account', sendAccount);
                    if (sendMailbox) fd.append('mailbox', sendMailbox);
                    composeAttachments.forEach(f => fd.append('attachments', f));
                    inlineImages.forEach(img => {
                        fd.append('inline_cids', img.cid);
                        fd.append('inline_images', new File([img.blob], img.filename, { type: img.mimeType }));
                    });
                    resp = await fetch('/api/mail/send', { method: 'POST', credentials: 'include', body: fd });
                } else {
                    const payload = { from, to, cc, bcc, subject, body, contentType, account: sendAccount };
                    if (sendMailbox) payload.mailbox = sendMailbox;
                    resp = await fetch('/api/mail/send', { method: 'POST', credentials: 'include', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
                }
                checkResponse(resp);
                if (resp.ok) {
                    await deleteActiveDraft();
                    clearComposeForm();
                    refreshSidebarCounts(sendAccount);
                    showMails(new Event('click'));
                } else {
                    const text = await resp.text();
                    showToast('Send failed: ' + text, 'error');
                }
            } finally {
                sendBtn.classList.remove('sending');
                sendBtn.disabled = false;
            }
        });
    }

    const draftBtn = document.getElementById('compose-draft-btn');
    if (draftBtn) {
        const draftBtnLabel = draftBtn.innerHTML;
        draftBtn.addEventListener('click', async () => {
            const fromValue = (document.getElementById('compose-from').value || '').trim();
            let name = '';
            let email = fromValue;
            const m = fromValue.match(/^(.*)\s+<(.+)>$/);
            if (m) { name = m[1].trim(); email = m[2].trim(); }
            const from = { name, email };
            const to = document.getElementById('compose-to').value.split(',').map(s => s.trim()).filter(Boolean);
            const cc = document.getElementById('compose-cc').value.split(',').map(s => s.trim()).filter(Boolean);
            const bcc = document.getElementById('compose-bcc').value.split(',').map(s => s.trim()).filter(Boolean);
            const subject = document.getElementById('compose-subject').value;
            const { body, contentType } = getComposeBody();

            // Resolve target account from From-address so the draft lands in the
            // owning account's Drafts mailbox, not the currently-viewed account.
            let draftAccount = currentMailbox.account;
            const fromLower = (email || '').toLowerCase();
            if (fromLower) {
                for (const acc of (accountsData || [])) {
                    const match = (acc.from_addresses || []).some(fa => {
                        const e = (fa.email || fa.Email || '').toLowerCase();
                        return e === fromLower;
                    });
                    if (match) { draftAccount = acc.Name; break; }
                }
            }

            const payload = { from, to, cc, bcc, subject, body, contentType, account: draftAccount, mailbox: currentMailbox.mailbox };

            draftBtn.disabled = true;
            draftBtn.textContent = 'Saving...';
            try {
                const resp = await fetch('/api/mail/draft', { method: 'POST', credentials: 'include', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
                checkResponse(resp);
                if (resp.ok) {
                    const data = await resp.json();
                    // Replace the previous draft only once the new copy exists.
                    await deleteActiveDraft();
                    const savedAccount = payload.account || data.account;
                    const savedMailbox = data.mailbox || 'Drafts';
                    if (data.uid) {
                        activeDraft = { account: savedAccount, mailbox: savedMailbox, uid: data.uid };
                    }
                    // Reflect new draft in sidebar counts and current view if the user
                    // happens to be on the Drafts mailbox.
                    refreshSidebarCounts(savedAccount);
                    if (currentMailbox.account === savedAccount && currentMailbox.mailbox === savedMailbox) {
                        try {
                            const newData = await fetchMails(savedAccount, savedMailbox, currentPage);
                            lastMailsData[`${savedAccount}-${savedMailbox}`] = newData;
                            renderMails(savedAccount, savedMailbox, newData, null, true);
                        } catch (_) {}
                    }
                    showToast('Draft saved to ' + savedMailbox);
                } else {
                    const text = (await resp.text()).trim();
                    showToast('Failed to save draft: ' + text, 'error');
                }
            } finally {
                draftBtn.innerHTML = draftBtnLabel;
                draftBtn.disabled = false;
            }
        });
    }
});

let currentMailbox = { account: '', mailbox: '' };

let accountsData = [];

let lastMailsData = {};

let lastMailboxStatus = {}; // track Messages+Unseen per "account-mailbox" to skip redundant mails fetch

let showUnreadOnly = false;

let showSpamScores = localStorage.getItem('go-mail-spam-scores') === 'true';

let selectAllInMailbox = false; // true when user selected all emails in mailbox (not just visible page)

let showMailboxCounts = localStorage.getItem('go-mail-show-mailbox-counts') === 'true';

let currentPage = 1;

let PAGE_SIZE = parseInt(localStorage.getItem('go-mail-page-size')) || 0; // 0 means auto-calculate

let selectedUIDs = new Set();

let mailboxSettingsMap = {}; // mailbox name (lowercase) -> icon

let prevUnseenCounts = {}; // account name -> unseen count for notification detection

let notifiedUIDs = {}; // account name -> Set of UIDs we already notified about

let newMailSoundEnabled = false;

// App-level notification gate (on top of browser permission), persisted per-browser.
    // Default: on when permission already granted, else off.
    let notificationsEnabled = (function() {
const stored = localStorage.getItem('go-mail-notifications-enabled');
if (stored !== null) return stored === 'true';
return ('Notification' in window) && Notification.permission === 'granted';
    })();

let spamMailboxName = 'Junk';

let filtersHaveRun = false; // true after first filter run completes

let initialNotifsDone = false; // true after first post-filter notification batch

// New mail sound using Web Audio API — short two-tone chime
    let audioCtx = null;

updateNotifIcon();

document.getElementById('notif-toggle-btn').addEventListener('click', async () => {
if (!('Notification' in window)) return;
if (Notification.permission === 'granted') {
    // Permission already granted: toggle the app-level gate.
    notificationsEnabled = !notificationsEnabled;
    localStorage.setItem('go-mail-notifications-enabled', notificationsEnabled);
    updateNotifIcon();
    return;
}
if (Notification.permission === 'denied') {
    showToast('Notifications are blocked. Please allow notifications in your browser settings.', 'warning', 6000);
    return;
}
// Permission not yet decided: request it, enable on grant.
const result = await Notification.requestPermission();
if (result === 'granted') {
    notificationsEnabled = true;
    localStorage.setItem('go-mail-notifications-enabled', notificationsEnabled);
} else if (result === 'denied') {
    showToast('Notifications are blocked. Please allow notifications in your browser settings.', 'warning', 6000);
}
updateNotifIcon();
    });

updateSoundIcon();

document.getElementById('sound-toggle-btn').addEventListener('click', () => {
newMailSoundEnabled = !newMailSoundEnabled;
localStorage.setItem('go-mail-sound-enabled', newMailSoundEnabled);
updateSoundIcon();
if (newMailSoundEnabled) playNewMailSound(); // preview
    });

// Thread collapse state persistence
    const THREAD_STATE_KEY = 'go-mail-thread-state';

main().then(() => {
    window.addEventListener('hashchange', () => {
        renderFromHash();
    });
    renderFromHash();

    // Delegated click handler for mail rows
    const mailsContainer = document.getElementById('mails-container');
    if (mailsContainer) {
        mailsContainer.addEventListener('click', async (evt) => {
            if (evt.target.closest('.mailbox-header')) {
                return;
            }

            const row = evt.target.closest('.mail-row');
            if (!row) return;

            // Don't handle clicks on toggle thread buttons or checkboxes
            if (evt.target.closest('[onclick]')) return;
            if (evt.target.classList.contains('mail-select-checkbox')) return;

            evt.preventDefault();

            // Multi-select mode: clicking a row toggles its checkbox instead of opening it
            if (selectedUIDs.size > 0 || selectAllInMailbox) {
                const cb = row.querySelector('.mail-select-checkbox');
                if (cb) cb.click();
                return;
            }

            // Handle selection
            const currentlySelected = mailsContainer.querySelector('.mail-row.selected');
            if (currentlySelected) {
                currentlySelected.classList.remove('selected');
            }
            row.classList.add('selected');

            const accountName = row.dataset.account;
            const mailboxName = row.dataset.mailbox;
            const uid = row.dataset.uid;

            const account = accountsData.find(acc => acc.Name === accountName);
            if (account && account.draft_mailbox && account.draft_mailbox.toLowerCase() === mailboxName.toLowerCase()) {
                openDraft(accountName, mailboxName, uid);
                return;
            }

            let mailsData = null;
            try {
                mailsData = await fetchMails(accountName, mailboxName, currentPage);
            } catch (err) {
                mailsData = null;
            }

            let mail = null;
            if (mailsData && mailsData.conversations) {
                for (const conv of mailsData.conversations) {
                    mail = conv.emails.find(m => m.uid == uid);
                    if (mail) break;
                }
            }

            if (mail && (!mail.flags || !mail.flags.includes('\\Seen'))) {
                fetch(`/api/mail/seen?account=${encodeURIComponent(accountName)}&mailbox=${encodeURIComponent(mailboxName)}&uid=${uid}`, { method: 'POST', credentials: 'include' });

                // Update the row styling
                row.classList.remove('unread');
                row.classList.add('read');

                const mailboxSpans = document.querySelectorAll(`span[data-account="${accountName}"][data-mailbox="${mailboxName}"]`);
                mailboxSpans.forEach(mailboxSpan => {
                    const totalCountSpan = mailboxSpan.querySelector('.total-count');
                    if (totalCountSpan) {
                        // In total-count mode, update the unseen/total display
                        const match = totalCountSpan.innerHTML.match(/(\d+)\/(\d+)/);
                        if (match) {
                            let unseen = parseInt(match[1], 10) - 1;
                            const total = parseInt(match[2], 10);
                            totalCountSpan.innerHTML = unseen > 0 ? `<b>${unseen}</b>/${total}` : `${total}`;
                        }
                    } else {
                        const unreadCountSpan = mailboxSpan.querySelector('.unread-count');
                        if (unreadCountSpan) {
                            let unread = parseInt(unreadCountSpan.innerText, 10);
                            if (unread > 0) {
                                unread--;
                                if (unread > 0) {
                                    unreadCountSpan.innerText = unread;
                                } else {
                                    unreadCountSpan.remove();
                                    mailboxSpan.style.fontWeight = 'normal';
                                }
                            }
                        }
                    }
                });

                const accountButtons = document.querySelectorAll('.account-tablinks');
                accountButtons.forEach(button => {
                    if (button.dataset.account === accountName) {
                        const badge = button.querySelector('.account-unread-badge');
                        if (badge) {
                            let unread = parseInt(badge.textContent, 10);
                            if (unread > 0) unread--;
                            if (unread > 0) {
                                badge.textContent = unread;
                            } else {
                                badge.remove();
                            }
                        }
                    }
                });

                if (!mail.flags) mail.flags = [];
                mail.flags.push('\\Seen');
            }

            showMail(accountName, mailboxName, uid, evt);
        });

        // Keyboard activation mirrors the row click; inner controls keep their own keys.
        mailsContainer.addEventListener('keydown', (evt) => {
            if (evt.key !== 'Enter' && evt.key !== ' ') return;
            const row = evt.target.closest('.mail-row');
            if (!row || evt.target !== row) return;
            evt.preventDefault();
            row.click();
        });
    }

    // Filters UI hooks
    const condContainerEl = document.getElementById('filter-conditions');
    if (condContainerEl) attachContainerDnd(condContainerEl);
    const actContainerEl = document.getElementById('filter-actions');
    if (actContainerEl) attachContainerDnd(actContainerEl);

    const newFilterBtn = document.getElementById('new-filter-btn');
    if (newFilterBtn) newFilterBtn.addEventListener('click', openNewFilter);
    const filterSearch = document.getElementById('filter-search');
    if (filterSearch) filterSearch.addEventListener('input', renderFilteredList);
    const refreshFiltersBtn = document.getElementById('refresh-filters');
    if (refreshFiltersBtn) refreshFiltersBtn.addEventListener('click', loadFilters);
    const runAllFiltersBtn = document.getElementById('run-all-filters');
    if (runAllFiltersBtn) runAllFiltersBtn.addEventListener('click', async () => {
        runAllFiltersBtn.disabled = true;
        runAllFiltersBtn.textContent = 'Running...';
        try {
            const resp = await fetch('/api/filters/run-all', { method: 'POST' });
            const data = await resp.json();
            if (data.status === 'already_running') {
                runAllFiltersBtn.textContent = 'Already Running';
            } else {
                runAllFiltersBtn.textContent = 'Triggered!';
            }
        } catch (e) {
            runAllFiltersBtn.textContent = 'Error';
        }
        setTimeout(() => {
            runAllFiltersBtn.disabled = false;
            runAllFiltersBtn.textContent = 'Run All Filters';
        }, 2000);
    });
    const saveFilterBtn = document.getElementById('save-filter');
    if (saveFilterBtn) saveFilterBtn.addEventListener('click', saveFilter);
    const cancelFilterBtn = document.getElementById('cancel-filter');
    if (cancelFilterBtn) cancelFilterBtn.addEventListener('click', () => { openTab(null, 'filters-content', 'mail-tabcontent', 'mail-tablinks'); loadFilters(); });
    const backToFiltersBtn = document.getElementById('back-to-filters');
    if (backToFiltersBtn) backToFiltersBtn.addEventListener('click', () => { openTab(null, 'filters-content', 'mail-tabcontent', 'mail-tablinks'); loadFilters(); });

    // Labels UI hooks
    const labelSearch = document.getElementById('label-search');
    if (labelSearch) labelSearch.addEventListener('input', renderFilteredLabels);
    const newLabelBtn = document.getElementById('new-label-btn');
    if (newLabelBtn) newLabelBtn.addEventListener('click', openNewLabel);
    const saveLabelBtn = document.getElementById('save-label');
    if (saveLabelBtn) saveLabelBtn.addEventListener('click', saveLabel);
    const cancelLabelBtn = document.getElementById('cancel-label');
    if (cancelLabelBtn) cancelLabelBtn.addEventListener('click', () => { openTab(null, 'labels-content', 'mail-tabcontent', 'mail-tablinks'); loadLabels(); });
    const backToLabelsBtn = document.getElementById('back-to-labels');
    if (backToLabelsBtn) backToLabelsBtn.addEventListener('click', () => { openTab(null, 'labels-content', 'mail-tabcontent', 'mail-tablinks'); loadLabels(); });
    const labelColorInput = document.getElementById('label-color');
    if (labelColorInput) labelColorInput.addEventListener('input', () => {
        document.getElementById('label-preview').style.backgroundColor = labelColorInput.value;
    });
    const labelNameInput = document.getElementById('label-name');
    if (labelNameInput) labelNameInput.addEventListener('input', () => {
        document.getElementById('label-preview').textContent = labelNameInput.value || 'Preview';
    });
    const addLabelAccountBtn = document.getElementById('add-label-account-btn');
    if (addLabelAccountBtn) addLabelAccountBtn.addEventListener('click', () => {
        const sel = document.getElementById('label-account-input');
        const val = sel.value;
        if (val && !labelEditorAccounts.includes(val)) {
            labelEditorAccounts.push(val);
            renderTagList('label-accounts-list', labelEditorAccounts);
        }
    });

    // Load labels data on init
    loadLabelsData();
});

// Cached list of plugin names (populated on first filter editor open).
// Reads the new /api/plugins shape which nests status under each row.
let pluginNamesCache = null;

// ==================== Plugins Management ====================
// Mirrors the labels-management pattern: a list tab + an editor tab.
// The list refreshes every 5s while visible so status changes (e.g. a
// crash + auto-restart) show without operator action.
let pluginsData = [];

let pluginEditorMode = 'edit'; // 'edit' | 'create'

let pluginEditorName = null; // current plugin name in edit mode

let pluginEditorSecretPaths = []; // dotted paths the server masked

let pluginsRefreshTimer = null;

const PLUGINS_REFRESH_MS = 5000;

// Wire up the static buttons after the DOM is in place. The list+detail
// panes are part of the SPA shell, so a single DOMContentLoaded run is
// enough — no need to re-bind when switching tabs.
document.addEventListener('DOMContentLoaded', () => {
    const newBtn = document.getElementById('new-plugin-btn');
    if (newBtn) newBtn.addEventListener('click', openNewPluginEditor);
    const refreshBtn = document.getElementById('refresh-plugins');
    if (refreshBtn) refreshBtn.addEventListener('click', () => loadPlugins());

    const saveBtn = document.getElementById('save-plugin');
    if (saveBtn) saveBtn.addEventListener('click', () => savePlugin(false));
    const saveRestartBtn = document.getElementById('save-restart-plugin');
    if (saveRestartBtn) saveRestartBtn.addEventListener('click', () => savePlugin(true));
    const restartBtn = document.getElementById('restart-plugin');
    if (restartBtn) restartBtn.addEventListener('click', restartPluginFromEditor);
    const triggerBtn = document.getElementById('trigger-plugin');
    if (triggerBtn) triggerBtn.addEventListener('click', triggerPluginFromEditor);
    const toggleBtn = document.getElementById('toggle-plugin-disabled');
    if (toggleBtn) toggleBtn.addEventListener('click', togglePluginDisabledAction);
    const delBtn = document.getElementById('delete-plugin');
    if (delBtn) delBtn.addEventListener('click', deletePluginAction);
    const cancelBtn = document.getElementById('cancel-plugin');
    if (cancelBtn) cancelBtn.addEventListener('click', () => openTab(null, 'plugins-content', 'mail-tabcontent', 'mail-tablinks'));
    const backToPluginsBtn = document.getElementById('back-to-plugins');
    if (backToPluginsBtn) backToPluginsBtn.addEventListener('click', () => openTab(null, 'plugins-content', 'mail-tabcontent', 'mail-tablinks'));
});

// Cached list of label names (populated on first filter editor open).
let labelNamesCache = null;

let editorState = {
    accounts: [],
    mailboxes: [],
};

(function wireEditorButtons() {
    const addAccountBtn = document.getElementById('add-account-btn');
    const accountInput = document.getElementById('filter-account-input');
    if (addAccountBtn && accountInput) {
        addAccountBtn.addEventListener('click', () => {
            const v = accountInput.value.trim();
            if (!v) return;
            editorState.accounts.push(v);
            accountInput.value = '';
            renderTagList('filter-accounts-list', editorState.accounts);
        });
    }

    const addMailboxBtn = document.getElementById('add-mailbox-btn');
    const mailboxInput = document.getElementById('filter-mailbox-input');
    if (addMailboxBtn && mailboxInput) {
        addMailboxBtn.addEventListener('click', () => {
            const v = mailboxInput.value.trim();
            if (!v) return;
            editorState.mailboxes.push(v);
            mailboxInput.value = '';
            renderTagList('filter-mailboxes-list', editorState.mailboxes);
        });
    }

    const activeUntilInput = document.getElementById('filter-active-until');
    if (activeUntilInput) activeUntilInput.addEventListener('input', updateOnExpireVisibility);

    const addCondBtn = document.getElementById('add-condition-btn');
    if (addCondBtn) addCondBtn.addEventListener('click', () => {
        const container = document.getElementById('filter-conditions');
        const row = makeConditionRow({}, () => {});
        container.appendChild(row);
    });

    const addActBtn = document.getElementById('add-action-btn');
    if (addActBtn) addActBtn.addEventListener('click', () => {
        const container = document.getElementById('filter-actions');
        const row = makeActionRow({}, () => {});
        container.appendChild(row);
    });

    populateFilterEditorDropdowns();
})();

const sf = document.getElementById('save-filter');

if (sf) {
    sf.removeEventListener && sf.removeEventListener('click', saveFilter);
    sf.addEventListener('click', saveFilter);
}

document.getElementById('mark-unread-button').addEventListener('click', async () => {
    const mailContentDiv = document.getElementById('mail-content');
    const uid = mailContentDiv.dataset.uid;
    const accountName = mailContentDiv.dataset.account;
    const mailboxName = mailContentDiv.dataset.mailbox;
    const res = await fetch(`/api/mail/unseen?account=${encodeURIComponent(accountName)}&mailbox=${encodeURIComponent(mailboxName)}&uid=${uid}`, { method: 'POST', credentials: 'include' });
    checkResponse(res);
    document.getElementById("defaultMailOpen").click();
    refreshSidebarCounts(accountName);
});

document.getElementById('delete-button').addEventListener('click', async () => {
    const mailContentDiv = document.getElementById('mail-content');
    const uid = mailContentDiv.dataset.uid;
    const accountName = mailContentDiv.dataset.account;
    const mailboxName = mailContentDiv.dataset.mailbox;

    if (await showConfirm('Are you sure you want to delete this email?', 'Delete')) {
        const res = await fetch(`/api/mail/delete?account=${encodeURIComponent(accountName)}&mailbox=${encodeURIComponent(mailboxName)}&uid=${uid}`, { method: 'POST', credentials: 'include' });
        checkResponse(res);
        document.getElementById("defaultMailOpen").click();
        refreshSidebarCounts(accountName);
    }
});

document.getElementById('move-to-button').addEventListener('click', async () => {
    const mailContentDiv = document.getElementById('mail-content');
    const accountName = mailContentDiv.dataset.account;
    let mailboxData = await fetchMailboxes(accountName);
    let mailboxes = mailboxData.mailboxes;
    const mailboxList = document.getElementById('mailbox-list');
    mailboxList.innerHTML = '';

    const inboxIndex = mailboxes.findIndex(m => m.toUpperCase() === 'INBOX');
    if (inboxIndex > -1) {
        const inbox = mailboxes.splice(inboxIndex, 1)[0];
        mailboxes.sort((a, b) => a.localeCompare(b));
        mailboxes.unshift(inbox);
    } else {
        mailboxes.sort((a, b) => a.localeCompare(b));
    }

    for (const mailbox of mailboxes) {
        const li = document.createElement('li');
        li.innerText = mailbox;
        li.addEventListener('click', async () => {
            const uid = mailContentDiv.dataset.uid;
            const fromMailbox = mailContentDiv.dataset.mailbox;
            const res = await fetch(`/api/mail/move?account=${encodeURIComponent(accountName)}&from=${encodeURIComponent(fromMailbox)}&to=${encodeURIComponent(mailbox)}&uid=${uid}`, { method: 'POST', credentials: 'include' });
            checkResponse(res);
            document.getElementById('move-to-overlay').style.display = 'none';
            document.getElementById('move-to-modal').style.display = 'none';
            document.getElementById("defaultMailOpen").click();
        });
        mailboxList.appendChild(li);
    }
    document.getElementById('move-to-overlay').style.display = 'block';
    document.getElementById('move-to-modal').style.display = 'block';
});

document.getElementById('cancel-move-button').addEventListener('click', () => {
    document.getElementById('move-to-overlay').style.display = 'none';
    document.getElementById('move-to-modal').style.display = 'none';
});

document.getElementById('spam-button').addEventListener('click', async () => {
    const mailContentDiv = document.getElementById('mail-content');
    const uid = parseInt(mailContentDiv.dataset.uid);
    const accountName = mailContentDiv.dataset.account;
    const mailboxName = mailContentDiv.dataset.mailbox;
    const isJunk = mailboxName.toLowerCase() === spamMailboxName.toLowerCase();

    if (isJunk) {
        // "Not Spam" — train as ham and move to INBOX
        const res = await fetch('/api/spam/train', {
            method: 'POST', credentials: 'include',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ account: accountName, mailbox: mailboxName, uid: uid, spam: false })
        });
        checkResponse(res);
        const moveRes = await fetch(`/api/mail/move?account=${encodeURIComponent(accountName)}&from=${encodeURIComponent(mailboxName)}&to=INBOX&uid=${uid}`, { method: 'POST', credentials: 'include' });
        checkResponse(moveRes);
        document.getElementById("defaultMailOpen").click();
        refreshSidebarCounts(accountName);
    } else {
        // "Report Spam" — train as spam and move to spam mailbox
        if (await showConfirm('Report this email as spam?', 'Report')) {
            const res = await fetch('/api/spam/train', {
                method: 'POST', credentials: 'include',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ account: accountName, mailbox: mailboxName, uid: uid, spam: true })
            });
            checkResponse(res);
            const moveRes = await fetch(`/api/mail/move?account=${encodeURIComponent(accountName)}&from=${encodeURIComponent(mailboxName)}&to=${encodeURIComponent(spamMailboxName)}&uid=${uid}`, { method: 'POST', credentials: 'include' });
            checkResponse(moveRes);
            document.getElementById("defaultMailOpen").click();
            refreshSidebarCounts(accountName);
        }
    }
});

document.getElementById('not-spam-button').addEventListener('click', async () => {
    const mailContentDiv = document.getElementById('mail-content');
    const uid = parseInt(mailContentDiv.dataset.uid);
    const accountName = mailContentDiv.dataset.account;
    const mailboxName = mailContentDiv.dataset.mailbox;

    const res = await fetch('/api/spam/train', {
        method: 'POST', credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ account: accountName, mailbox: mailboxName, uid: uid, spam: false })
    });
    checkResponse(res);
    showToast('Trained as not spam', 'success');
    document.getElementById('not-spam-button').style.display = 'none';
    // Refresh the spam score badge
    const scoreBadge = document.getElementById('spam-score-badge');
    fetch(`/api/spam/classify?account=${encodeURIComponent(accountName)}&mailbox=${encodeURIComponent(mailboxName)}&uid=${uid}`, { credentials: 'include' })
        .then(r => r.ok ? r.json() : null)
        .then(data => {
            if (!data || !data.trained) return;
            const pct = Math.round(data.score * 100);
            scoreBadge.textContent = pct + '% spam';
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
            if (pct > 0) {
                document.getElementById('not-spam-button').style.display = 'inline-flex';
            }
        });
});

// Search scope toggle button
document.getElementById('search-scope').addEventListener('click', function() {
    if (this.dataset.scope === 'all') {
        this.dataset.scope = 'current';
        this.textContent = currentMailbox.mailbox || 'Current';
        this.title = 'Search scope: Current mailbox (click to search all)';
        this.classList.add('scope-current');
    } else {
        this.dataset.scope = 'all';
        this.textContent = 'All';
        this.title = 'Search scope: All mailboxes (click to limit to current)';
        this.classList.remove('scope-current');
    }
});

document.getElementById('search-scope').dataset.scope = 'all';

// Search operator autocomplete: suggests operators, contacts (after
// from:/to:), and labels (after label:). Enter runs the search when the
// dropdown is closed; navigates/selects when it's open.
(function() {
    const input = document.getElementById('search-input');
    const dropdown = document.getElementById('ac-search');
    if (!input || !dropdown) return;

    const OPERATORS = ['from:', 'to:', 'subject:', 'label:', 'has:attachment', 'is:unread', 'is:read', 'newer_than:', 'older_than:'];
    let searchContacts = [];
    let currentItems = [];
    let selectedIdx = -1;

    fetch('/api/contacts', { credentials: 'include' })
        .then(r => r.ok ? r.json() : [])
        .then(list => { searchContacts = list || []; })
        .catch(() => { /* ignore */ });

    // The whitespace-delimited token under the cursor.
    function tokenBounds() {
        const cursor = input.selectionStart;
        const before = input.value.slice(0, cursor);
        const start = Math.max(before.lastIndexOf(' '), before.lastIndexOf('\t')) + 1;
        return { start, cursor, token: before.slice(start) };
    }

    function labelsForAccount() {
        const acct = currentMailbox.account;
        return (labelsData || [])
            .filter(l => !l.hidden)
            .filter(l => !l.accounts || l.accounts.length === 0 || l.accounts.includes(acct))
            .map(l => l.name);
    }

    const quoteIfNeeded = v => /\s/.test(v) ? '"' + v + '"' : v;

    function computeItems(token) {
        const colon = token.indexOf(':');
        if (colon > 0) {
            const op = token.slice(0, colon).toLowerCase();
            const partial = token.slice(colon + 1).replace(/^"/, '').toLowerCase();
            if (op === 'from' || op === 'to') {
                return searchContacts
                    .filter(e => e.toLowerCase().includes(partial))
                    .slice(0, 8)
                    .map(e => ({ display: e, insert: op + ':' + e + ' ' }));
            }
            if (op === 'label') {
                return labelsForAccount()
                    .filter(n => n.toLowerCase().includes(partial))
                    .slice(0, 8)
                    .map(n => ({ display: n, insert: 'label:' + quoteIfNeeded(n) + ' ' }));
            }
            return []; // subject/has/is values: nothing to suggest
        }
        const tl = token.toLowerCase();
        return OPERATORS
            .filter(op => op.includes(tl))
            .map(op => ({ display: op, insert: op.endsWith(':') ? op : op + ' ', reopen: op.endsWith(':') }));
    }

    function render(items, token) {
        dropdown.innerHTML = '';
        selectedIdx = -1;
        currentItems = items;
        if (!items.length) { dropdown.classList.remove('active'); return; }
        const ql = token.replace(/^[^:]*:/, '').replace(/^"/, '').toLowerCase();
        items.forEach((it, i) => {
            const div = document.createElement('div');
            div.className = 'autocomplete-item';
            const idx = ql ? it.display.toLowerCase().indexOf(ql) : -1;
            renderHighlightedText(div, it.display, idx, ql.length);
            div.addEventListener('mousedown', (e) => { e.preventDefault(); choose(i); });
            dropdown.appendChild(div);
        });
        dropdown.classList.add('active');
    }

    function update() {
        const { token } = tokenBounds();
        render(computeItems(token), token);
    }

    function choose(i) {
        const it = currentItems[i];
        if (!it) return;
        const { start, cursor } = tokenBounds();
        const v = input.value;
        const head = v.slice(0, start) + it.insert;
        input.value = head + v.slice(cursor);
        input.setSelectionRange(head.length, head.length);
        input.focus();
        if (it.reopen) { update(); } else { close(); }
    }

    function close() {
        dropdown.classList.remove('active');
        dropdown.innerHTML = '';
        selectedIdx = -1;
        currentItems = [];
    }

    input.addEventListener('input', update);

    input.addEventListener('keydown', (e) => {
        const items = dropdown.querySelectorAll('.autocomplete-item');
        const open = dropdown.classList.contains('active') && items.length > 0;
        if (open) {
            if (e.key === 'ArrowDown') {
                e.preventDefault();
                selectedIdx = Math.min(selectedIdx + 1, items.length - 1);
                items.forEach((it, i) => it.classList.toggle('selected', i === selectedIdx));
                items[selectedIdx].scrollIntoView({ block: 'nearest' });
                return;
            }
            if (e.key === 'ArrowUp') {
                e.preventDefault();
                selectedIdx = Math.max(selectedIdx - 1, 0);
                items.forEach((it, i) => it.classList.toggle('selected', i === selectedIdx));
                items[selectedIdx].scrollIntoView({ block: 'nearest' });
                return;
            }
            if ((e.key === 'Enter' || e.key === 'Tab') && selectedIdx >= 0) {
                e.preventDefault();
                choose(selectedIdx);
                return;
            }
            if (e.key === 'Escape') { e.preventDefault(); close(); return; }
        }
        if (e.key === 'Enter') { e.preventDefault(); close(); runSearch(); }
    });

    input.addEventListener('blur', () => { setTimeout(close, 150); });
})();

// refreshMailState re-reads accounts, unseen counts and the open mailbox.
// Runs every 60s as a fallback and immediately on a server-sent event.
let refreshInFlight = false;

let refreshRequested = false;

setInterval(refreshMailState, 60000);

// Server-sent events: the backend publishes after an IMAP IDLE notice
// or a filter run, so new mail shows up without waiting for the poll.
(function() {
    if (!('EventSource' in window)) return;
    const es = new EventSource('/api/events');
    es.onmessage = () => { refreshMailState(); };
    es.onerror = () => { /* EventSource reconnects on its own */ };
})();

// Poll filter running status
setInterval(async () => {
    try {
        const res = await fetch('/api/filters/status', { credentials: 'include' });
        if (res.ok) {
            const data = await res.json();
            const ind = document.getElementById('filters-running-indicator');
            if (ind) ind.style.display = data.running ? '' : 'none';
            if (data.have_run) filtersHaveRun = true;
        }
    } catch (_) {}
}, 3000);

// Attachment handling
(function() {
    const attachBtn = document.getElementById('compose-attach-btn');
    const fileInput = document.getElementById('compose-file-input');
    attachBtn.addEventListener('click', () => fileInput.click());
    fileInput.addEventListener('change', () => {
        for (const file of fileInput.files) {
            composeAttachments.push(file);
        }
        fileInput.value = '';
        renderAttachments();
    });
})();

// Rich text editor toolbar
(function() {
    const toolbar = document.getElementById('compose-toolbar');
    const editor = document.getElementById('compose-body-html');
    const textarea = document.getElementById('compose-body');
    const modeToggle = document.getElementById('compose-mode-toggle');
    const modeLabel = document.getElementById('compose-mode-label');

    // Toolbar button commands
    toolbar.addEventListener('click', (e) => {
        const btn = e.target.closest('.compose-toolbar-btn');
        if (!btn || btn === modeToggle) return;
        if (btn.classList.contains('color-picker-label')) return;

        const cmd = btn.dataset.cmd;
        if (!cmd) return;
        e.preventDefault();

        document.execCommand(cmd, false, null);
        editor.focus();
    });

    // Text color picker
    const foreColorInput = document.getElementById('compose-fore-color');
    const foreColorUnderline = document.getElementById('fore-color-underline');
    const foreColorPreview = document.getElementById('fore-color-preview');
    foreColorInput.addEventListener('input', (e) => {
        document.execCommand('foreColor', false, e.target.value);
        foreColorUnderline.style.background = e.target.value;
        foreColorPreview.style.color = e.target.value;
        editor.focus();
    });

    // Background highlight color picker
    const bgColorInput = document.getElementById('compose-bg-color');
    const bgColorUnderline = document.getElementById('bg-color-underline');
    const bgColorPreview = document.getElementById('bg-color-preview');
    bgColorInput.addEventListener('input', (e) => {
        document.execCommand('hiliteColor', false, e.target.value);
        bgColorUnderline.style.background = e.target.value;
        bgColorPreview.style.background = e.target.value;
        editor.focus();
    });

    // Font size select
    toolbar.addEventListener('change', (e) => {
        const sel = e.target.closest('.compose-toolbar-select');
        if (!sel) return;
        const cmd = sel.dataset.cmd;
        if (cmd && sel.value) {
            document.execCommand(cmd, false, sel.value);
            sel.value = '';
            editor.focus();
        }
    });

    // Link button
    document.getElementById('compose-link-btn').addEventListener('click', (e) => {
        e.preventDefault();
        const url = prompt('Enter URL:');
        if (url) {
            document.execCommand('createLink', false, url);
        }
        editor.focus();
    });

    // Mode toggle
    modeToggle.addEventListener('click', (e) => {
        e.preventDefault();
        if (composeMode === 'html') {
            // Switch to text mode
            composeMode = 'text';
            textarea.value = editor.innerText;
            editor.style.display = 'none';
            textarea.style.display = 'block';
            toolbar.querySelectorAll('.compose-toolbar-btn:not(.compose-mode-toggle), .compose-toolbar-select, .compose-toolbar-sep').forEach(el => el.style.visibility = 'hidden');
            modeLabel.textContent = 'Rich text';
        } else {
            // Switch to HTML mode
            composeMode = 'html';
            editor.innerHTML = textarea.value.replace(/\n/g, '<br>');
            textarea.style.display = 'none';
            editor.style.display = 'block';
            toolbar.querySelectorAll('.compose-toolbar-btn:not(.compose-mode-toggle), .compose-toolbar-select, .compose-toolbar-sep').forEach(el => el.style.visibility = 'visible');
            modeLabel.textContent = 'Plain text';
            editor.focus();
        }
    });
})();

// Contact autocomplete
(function() {
    let contactEmails = [];

    async function loadContacts() {
        try {
            const res = await fetch('/api/contacts', { credentials: 'include' });
            if (res.ok) contactEmails = await res.json();
        } catch (e) { /* ignore */ }
    }
    loadContacts();

    function setupAutocomplete(inputId, dropdownId) {
        const input = document.getElementById(inputId);
        const dropdown = document.getElementById(dropdownId);
        if (!input || !dropdown) return;
        let selectedIdx = -1;

        function getLastToken() {
            const val = input.value;
            const lastComma = val.lastIndexOf(',');
            return val.substring(lastComma + 1).trim();
        }

        function replaceLastToken(email) {
            const val = input.value;
            const lastComma = val.lastIndexOf(',');
            const prefix = lastComma >= 0 ? val.substring(0, lastComma + 1) + ' ' : '';
            input.value = prefix + email + ', ';
            dropdown.classList.remove('active');
            dropdown.innerHTML = '';
            selectedIdx = -1;
            input.focus();
        }

        function render(matches, query) {
            dropdown.innerHTML = '';
            selectedIdx = -1;
            if (matches.length === 0) {
                dropdown.classList.remove('active');
                return;
            }
            const ql = query.toLowerCase();
            matches.forEach((email, i) => {
                const div = document.createElement('div');
                div.className = 'autocomplete-item';
                const idx = email.toLowerCase().indexOf(ql);
                renderHighlightedText(div, email, idx, ql.length);
                div.addEventListener('mousedown', (e) => {
                    e.preventDefault();
                    replaceLastToken(email);
                });
                dropdown.appendChild(div);
            });
            dropdown.classList.add('active');
        }

        input.addEventListener('input', () => {
            const token = getLastToken();
            if (token.length < 1) {
                dropdown.classList.remove('active');
                dropdown.innerHTML = '';
                return;
            }
            const tl = token.toLowerCase();
            const matches = contactEmails.filter(e => e.toLowerCase().includes(tl)).slice(0, 8);
            render(matches, token);
        });

        input.addEventListener('keydown', (e) => {
            const items = dropdown.querySelectorAll('.autocomplete-item');
            if (!items.length || !dropdown.classList.contains('active')) return;

            if (e.key === 'ArrowDown') {
                e.preventDefault();
                selectedIdx = Math.min(selectedIdx + 1, items.length - 1);
                items.forEach((it, i) => it.classList.toggle('selected', i === selectedIdx));
                items[selectedIdx].scrollIntoView({ block: 'nearest' });
            } else if (e.key === 'ArrowUp') {
                e.preventDefault();
                selectedIdx = Math.max(selectedIdx - 1, 0);
                items.forEach((it, i) => it.classList.toggle('selected', i === selectedIdx));
                items[selectedIdx].scrollIntoView({ block: 'nearest' });
            } else if (e.key === 'Enter' || e.key === 'Tab') {
                if (selectedIdx >= 0 && selectedIdx < items.length) {
                    e.preventDefault();
                    const email = contactEmails.filter(em => em.toLowerCase().includes(getLastToken().toLowerCase()))[selectedIdx];
                    if (email) replaceLastToken(email);
                }
            } else if (e.key === 'Escape') {
                dropdown.classList.remove('active');
                dropdown.innerHTML = '';
                selectedIdx = -1;
            }
        });

        input.addEventListener('blur', () => {
            setTimeout(() => {
                dropdown.classList.remove('active');
                dropdown.innerHTML = '';
            }, 150);
        });
    }

    setupAutocomplete('compose-to', 'ac-compose-to');
    setupAutocomplete('compose-cc', 'ac-compose-cc');
    setupAutocomplete('compose-bcc', 'ac-compose-bcc');

    // Re-inject signature when From address changes
    document.getElementById('compose-from').addEventListener('change', () => {
        const htmlEditor = document.getElementById('compose-body-html');
        const textArea = document.getElementById('compose-body');
        // Remove existing signature
        const existingSig = htmlEditor.querySelector('.email-signature');
        if (existingSig) existingSig.remove();
        // Remove text signature (last occurrence after \n--)
        const txtVal = textArea.value;
        const sigIdx = txtVal.lastIndexOf('\n--');
        if (sigIdx > -1) {
            textArea.value = txtVal.substring(0, sigIdx);
        }
        insertSignature();
    });
})();

// Settings dropdown
(function() {
    const btn = document.getElementById('settings-btn');
    const menu = document.getElementById('settings-menu');
    btn.addEventListener('click', function(e) {
        e.stopPropagation();
        menu.classList.toggle('open');
    });
    document.addEventListener('click', function(e) {
        if (!menu.contains(e.target) && e.target !== btn) {
            menu.classList.remove('open');
        }
    });
})();

(function() {
    const btn = document.getElementById('logout-btn');
    if (!btn) return;
    btn.addEventListener('click', async function() {
        closeSettingsMenu();
        try {
            await fetch('/api/logout', { method: 'POST', credentials: 'include' });
        } finally {
            window.location.href = '/login';
        }
    });
})();

// UI-scale stepper — scales whole UI via zoom + viewport-unit compensation
(function() {
    const dec = document.getElementById('zoom-dec');
    const inc = document.getElementById('zoom-inc');
    const label = document.getElementById('zoom-value');
    if (!dec || !inc || !label) return;
    const STEPS = [0.8, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2];
    const saved = parseFloat(localStorage.getItem('go-mail-ui-scale')) || 1;
    let idx = STEPS.reduce((best, s, i) =>
        Math.abs(s - saved) < Math.abs(STEPS[best] - saved) ? i : best, 0);

    function apply() {
        const s = STEPS[idx];
        localStorage.setItem('go-mail-ui-scale', s);
        if (s === 1) {
            document.documentElement.style.removeProperty('--ui-scale');
        } else {
            document.documentElement.style.setProperty('--ui-scale', s);
        }
        label.textContent = Math.round(s * 100) + '%';
        dec.disabled = idx === 0;
        inc.disabled = idx === STEPS.length - 1;
    }
    // stopPropagation keeps the settings menu open while stepping
    dec.addEventListener('click', function(e) { e.stopPropagation(); if (idx > 0) { idx--; apply(); } });
    inc.addEventListener('click', function(e) { e.stopPropagation(); if (idx < STEPS.length - 1) { idx++; apply(); } });
    apply();
})();

// Theme toggle
(function() {
    const btn = document.getElementById('theme-toggle-btn');
    const iconSun = document.getElementById('theme-icon-sun');
    const iconMoon = document.getElementById('theme-icon-moon');

    const themeLabel = document.getElementById('theme-label');
    function updateIcons() {
        const isDark = document.documentElement.getAttribute('data-theme') === 'dark';
        iconSun.style.display = isDark ? 'block' : 'none';
        iconMoon.style.display = isDark ? 'none' : 'block';
        if (themeLabel) themeLabel.textContent = isDark ? 'Light mode' : 'Dark mode';
    }
    updateIcons();

    btn.addEventListener('click', function() {
        const current = document.documentElement.getAttribute('data-theme');
        const next = current === 'dark' ? 'light' : 'dark';
        document.documentElement.setAttribute('data-theme', next);
        localStorage.setItem('go-mail-theme', next);
        updateIcons();
    });
})();

const mobileMQ = window.matchMedia('(max-width: 600px)');

// Sidebar collapse (desktop only — mobile uses drawer)
(function() {
    const sidebar = document.getElementById('sidebar');
    const collapseBtn = document.getElementById('sidebar-collapse-btn');
    const COLLAPSE_KEY = 'go-mail-sidebar-collapsed';

    if (!mobileMQ.matches && localStorage.getItem(COLLAPSE_KEY) === 'true') {
        sidebar.classList.add('collapsed');
    }

    collapseBtn.addEventListener('click', function() {
        sidebar.classList.toggle('collapsed');
        localStorage.setItem(COLLAPSE_KEY, sidebar.classList.contains('collapsed'));
    });
})();

// Sidebar resize (desktop only)
(function() {
    const resizer = document.getElementById('sidebar-resizer');
    const sidebar = document.getElementById('sidebar');
    const STORAGE_KEY = 'go-mail-sidebar-width';
    const MIN_WIDTH = 120;
    const MAX_WIDTH = 500;

    const saved = localStorage.getItem(STORAGE_KEY);
    if (!mobileMQ.matches && saved) {
        const w = parseInt(saved, 10);
        if (w >= MIN_WIDTH && w <= MAX_WIDTH) {
            sidebar.style.width = w + 'px';
        }
    }

    // getBoundingClientRect/clientX are in zoomed px (html { zoom: --ui-scale });
    // inline style.width is unzoomed. Convert both by scale to avoid a jump.
    function uiScale() {
        const v = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--ui-scale'));
        return v > 0 ? v : 1;
    }

    let startX, startWidth;

    resizer.addEventListener('mousedown', function(e) {
        if (mobileMQ.matches) return;
        if (sidebar.classList.contains('collapsed')) return;
        e.preventDefault();
        const scale = uiScale();
        startX = e.clientX;
        startWidth = sidebar.getBoundingClientRect().width / scale;
        resizer.classList.add('resizing');
        document.body.style.cursor = 'col-resize';
        document.body.style.userSelect = 'none';

        function onMouseMove(e) {
            const w = Math.min(MAX_WIDTH, Math.max(MIN_WIDTH, startWidth + (e.clientX - startX) / scale));
            sidebar.style.width = w + 'px';
        }

        function onMouseUp() {
            resizer.classList.remove('resizing');
            document.body.style.cursor = '';
            document.body.style.userSelect = '';
            const finalWidth = Math.round(parseFloat(sidebar.style.width));
            if (!isNaN(finalWidth)) localStorage.setItem(STORAGE_KEY, finalWidth);
            document.removeEventListener('mousemove', onMouseMove);
            document.removeEventListener('mouseup', onMouseUp);
        }

        document.addEventListener('mousemove', onMouseMove);
        document.addEventListener('mouseup', onMouseUp);
    });
})();

// Mobile drawer: hamburger toggle, backdrop dismiss, auto-close on nav, ESC, breakpoint exit
(function() {
    const sidebar = document.getElementById('sidebar');
    const menuBtn = document.getElementById('mobile-menu-btn');
    const backdrop = document.getElementById('drawer-backdrop');

    function open() { document.body.classList.add('drawer-open'); }
    function close() { document.body.classList.remove('drawer-open'); }
    function isOpen() { return document.body.classList.contains('drawer-open'); }

    menuBtn.addEventListener('click', function(e) {
        e.stopPropagation();
        isOpen() ? close() : open();
    });
    backdrop.addEventListener('click', close);

    // Tapping any nav item inside the drawer closes it
    sidebar.addEventListener('click', function(e) {
        if (!mobileMQ.matches || !isOpen()) return;
        const t = e.target;
        if (t.closest('.account-tabcontent span[data-mailbox], .compose-btn')) {
            close();
        }
    });

    document.addEventListener('keydown', function(e) {
        if (e.key === 'Escape' && isOpen()) close();
    });

    // Leaving mobile range — make sure the drawer state can't leak
    mobileMQ.addEventListener('change', function(e) {
        if (!e.matches) close();
    });
})();
