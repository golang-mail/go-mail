// settings: function declarations shared globally with the other app/*.js files.

async function fetchMailboxSettings() {
    const response = await fetch('/api/mailbox-settings', { credentials: 'include' });
    checkResponse(response);
    const settings = await response.json();
    return settings;
}

// Refresh sidebar unseen counts for a specific account (or all if not specified).
async function refreshSidebarCounts(accountName) {
    try {
        const accounts = await fetchAccounts();
        const accountTabs = document.getElementById('account-tabs');
        const accountButtons = accountTabs.querySelectorAll('.account-tablinks');
        for (const account of accounts) {
            if (accountName && account.Name !== accountName) continue;
            // Update account badge
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
            // Update mailbox counts
            const mailboxData = await fetchMailboxes(account.Name);
            for (const mailbox of mailboxData.mailboxes) {
                const status = await fetchMailboxStatus(account.Name, mailbox);
                const spans = document.querySelectorAll(`span[data-account="${account.Name}"][data-mailbox="${mailbox}"]`);
                spans.forEach(span => updateMailboxCountBadge(span, status));
            }
        }
    } catch(err) { console.warn('Failed to refresh sidebar counts:', err); }
}

function playNewMailSound() {
if (!newMailSoundEnabled) return;
try {
    if (!audioCtx) audioCtx = new (window.AudioContext || window.webkitAudioContext)();
    const now = audioCtx.currentTime;
    // First tone
    const osc1 = audioCtx.createOscillator();
    const gain1 = audioCtx.createGain();
    osc1.type = 'sine';
    osc1.frequency.value = 880; // A5
    gain1.gain.setValueAtTime(0.15, now);
    gain1.gain.exponentialRampToValueAtTime(0.001, now + 0.15);
    osc1.connect(gain1);
    gain1.connect(audioCtx.destination);
    osc1.start(now);
    osc1.stop(now + 0.15);
    // Second tone (higher)
    const osc2 = audioCtx.createOscillator();
    const gain2 = audioCtx.createGain();
    osc2.type = 'sine';
    osc2.frequency.value = 1175; // D6
    gain2.gain.setValueAtTime(0.15, now + 0.12);
    gain2.gain.exponentialRampToValueAtTime(0.001, now + 0.3);
    osc2.connect(gain2);
    gain2.connect(audioCtx.destination);
    osc2.start(now + 0.12);
    osc2.stop(now + 0.3);
} catch (e) { /* audio not available */ }
    }

// Notification permission — must be triggered by user gesture in modern browsers
    function updateNotifIcon() {
const onIcon = document.getElementById('notif-icon-on');
const offIcon = document.getElementById('notif-icon-off');
const deniedIcon = document.getElementById('notif-icon-denied');
const btn = document.getElementById('notif-toggle-btn');
if (!onIcon || !offIcon || !deniedIcon || !btn) return;
if (!('Notification' in window)) {
    btn.style.display = 'none';
    return;
}
const perm = Notification.permission;
// When permission is granted, the on/off icon reflects the app-level toggle.
const showOn = perm === 'granted' && notificationsEnabled;
const showDefault = perm === 'default' || (perm === 'granted' && !notificationsEnabled);
onIcon.style.display = showOn ? '' : 'none';
offIcon.style.display = showDefault ? '' : 'none';
deniedIcon.style.display = perm === 'denied' ? '' : 'none';
const label = document.getElementById('notif-label');
if (perm === 'granted') {
    btn.title = notificationsEnabled ? 'Notifications on (click to disable)' : 'Notifications off (click to enable)';
    if (label) label.textContent = notificationsEnabled ? 'Notifications: On' : 'Notifications: Off';
} else if (perm === 'denied') {
    btn.title = 'Notifications blocked (change in browser settings)';
    if (label) label.textContent = 'Notifications: Blocked';
} else {
    btn.title = 'Enable browser notifications';
    if (label) label.textContent = 'Notifications';
}
    }

// New-mail sound toggle (persisted per-browser).
    function updateSoundIcon() {
const onIcon = document.getElementById('sound-icon-on');
const offIcon = document.getElementById('sound-icon-off');
const btn = document.getElementById('sound-toggle-btn');
const label = document.getElementById('sound-label');
if (!onIcon || !offIcon || !btn) return;
onIcon.style.display = newMailSoundEnabled ? '' : 'none';
offIcon.style.display = newMailSoundEnabled ? 'none' : '';
btn.title = newMailSoundEnabled ? 'New-mail sound on (click to mute)' : 'New-mail sound off (click to enable)';
if (label) label.textContent = newMailSoundEnabled ? 'Sound: On' : 'Sound: Off';
    }

function closeSettingsMenu() {
    document.getElementById('settings-menu').classList.remove('open');
}

// Settings dialog: per-account list of domains remote images are always loaded from.
async function openImageHosts() {
    const accounts = (typeof accountsData !== 'undefined' && accountsData) || await fetchAccounts();
    if (!accounts.length) return;
    const overlay = document.createElement('div');
    overlay.className = 'confirm-overlay';
    const dialog = document.createElement('div');
    dialog.className = 'confirm-dialog image-hosts-dialog';
    const title = document.createElement('div');
    title.className = 'confirm-message';
    title.textContent = 'Remote images are always loaded from these domains:';
    const select = document.createElement('select');
    select.className = 'image-hosts-account';
    accounts.forEach(a => {
        const opt = document.createElement('option');
        opt.value = a.Name;
        opt.textContent = a.Name;
        select.appendChild(opt);
    });
    const list = document.createElement('ul');
    list.className = 'image-hosts-list';
    const actions = document.createElement('div');
    actions.className = 'confirm-actions';
    const closeBtn = document.createElement('button');
    closeBtn.className = 'confirm-btn confirm-ok';
    closeBtn.textContent = 'Close';
    actions.appendChild(closeBtn);
    dialog.append(title, select, list, actions);
    overlay.appendChild(dialog);
    document.body.appendChild(overlay);
    requestAnimationFrame(() => overlay.classList.add('visible'));

    const close = () => {
        overlay.classList.remove('visible');
        setTimeout(() => overlay.remove(), 200);
    };
    closeBtn.onclick = close;
    overlay.addEventListener('click', (e) => { if (e.target === overlay) close(); });

    const render = async () => {
        const account = select.value;
        const hosts = [...await getAllowedImageHosts(account)].sort();
        list.textContent = '';
        if (hosts.length === 0) {
            const empty = document.createElement('li');
            empty.className = 'image-hosts-empty';
            empty.textContent = 'None. Remote images are blocked until you allow a domain from an email.';
            list.appendChild(empty);
            return;
        }
        hosts.forEach(host => {
            const li = document.createElement('li');
            const name = document.createElement('span');
            name.textContent = host;
            const rm = document.createElement('button');
            rm.type = 'button';
            rm.className = 'btn-icon';
            rm.title = 'Stop allowing ' + host;
            rm.textContent = '\u00d7';
            rm.addEventListener('click', async () => {
                try {
                    await removeImageHost(account, host);
                    render();
                } catch (err) {
                    showToast('Could not remove: ' + err.message, 'error');
                }
            });
            li.append(name, rm);
            list.appendChild(li);
        });
    };
    select.addEventListener('change', render);
    render();
}
