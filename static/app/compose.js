// compose: function declarations shared globally with the other app/*.js files.

function getSignatureForFrom(fromValue) {
    if (!fromValue || !accountsData) return { html: '', txt: '' };
    const emailMatch = fromValue.match(/<([^>]+)>/) || [null, fromValue.trim()];
    const email = emailMatch[1];
    for (const acc of accountsData) {
        if (!acc.from_addresses) continue;
        for (const fa of acc.from_addresses) {
            if (fa.email === email || fa.Email === email) {
                const perAddr = fa.signature || fa.Signature || '';
                const accHtml = acc.signature_html || '';
                const accTxt = acc.signature_txt || '';
                return {
                    html: accHtml || (perAddr ? perAddr.replace(/\n/g, '<br>') : ''),
                    txt: accTxt || perAddr
                };
            }
        }
    }
    return { html: '', txt: '' };
}

function insertSignature() {
    const fromValue = document.getElementById('compose-from').value;
    const sig = getSignatureForFrom(fromValue);
    const htmlEditor = document.getElementById('compose-body-html');
    const textArea = document.getElementById('compose-body');

    // Remove any existing signature
    const existingSig = htmlEditor.querySelector('.email-signature');
    if (existingSig) existingSig.remove();

    if (composeMode === 'html') {
        const sigContent = sig.html ? DOMPurify.sanitize(sig.html) : escapeHtml(sig.txt).replace(/\n/g, '<br>');
        if (sigContent) {
            htmlEditor.innerHTML += '<div class="email-signature"><br>' + sigContent + '</div>';
        }
    } else {
        const sigContent = sig.txt || sig.html.replace(/<br\s*\/?>/gi, '\n').replace(/<[^>]+>/g, '');
        if (sigContent) {
            textArea.value += '\n' + sigContent;
        }
    }
}

function clearComposeForm() {
    document.getElementById('compose-to').value = '';
    document.getElementById('compose-cc').value = '';
    document.getElementById('compose-bcc').value = '';
    document.getElementById('compose-subject').value = '';
    document.getElementById('compose-body').value = '';
    document.getElementById('compose-body-html').innerHTML = '';
    composeAttachments = [];
    activeDraft = null;
    replyContext = null;
    renderAttachments();
}

async function deleteActiveDraft() {
    if (!activeDraft) return;
    const { account, mailbox, uid } = activeDraft;
    activeDraft = null;
    try {
        await fetch(`/api/mail/delete?account=${encodeURIComponent(account)}&mailbox=${encodeURIComponent(mailbox)}&uid=${uid}`, { method: 'POST', credentials: 'include' });
    } catch (err) {
        console.error('Failed to delete draft:', err);
    }
}

function discardCompose(evt) {
    const hadDraft = !!activeDraft;
    if (hadDraft) {
        deleteActiveDraft();
    }
    clearComposeForm();
    showMails(evt);
}

function renderAttachments() {
    const container = document.getElementById('compose-attachments');
    container.innerHTML = '';
    composeAttachments.forEach((file, idx) => {
        const chip = document.createElement('span');
        chip.className = 'compose-attachment-chip';
        chip.innerHTML = svgAttachmentIcon(14);
        const nameSpan = document.createElement('span');
        nameSpan.textContent = file.name;
        chip.appendChild(nameSpan);
        const sizeSpan = document.createElement('span');
        sizeSpan.className = 'attachment-size';
        sizeSpan.textContent = '(' + formatFileSize(file.size) + ')';
        chip.appendChild(sizeSpan);
        const removeBtn = document.createElement('button');
        removeBtn.className = 'remove-attachment';
        removeBtn.title = 'Remove attachment';
        removeBtn.setAttribute('aria-label', 'Remove attachment');
        removeBtn.innerHTML = '\u00d7';
        removeBtn.addEventListener('click', () => {
            composeAttachments.splice(idx, 1);
            renderAttachments();
        });
        chip.appendChild(removeBtn);
        container.appendChild(chip);
    });
}

function getComposeBody() {
    if (composeMode === 'html') {
        // Extract inline data: images and convert to cid: references
        const html = document.getElementById('compose-body-html').innerHTML;
        const inlineImages = [];
        let idx = 0;
        const rewritten = html.replace(/(<img\s[^>]*src\s*=\s*["'])data:([^;]+);base64,([^"']+)(["'][^>]*>)/gi, (match, prefix, mimeType, b64, suffix) => {
            const cid = 'inline-img-' + idx + '-' + Date.now();
            idx++;
            // Convert base64 string to Blob
            const byteChars = atob(b64);
            const byteArr = new Uint8Array(byteChars.length);
            for (let i = 0; i < byteChars.length; i++) byteArr[i] = byteChars.charCodeAt(i);
            const ext = mimeType.split('/')[1] || 'png';
            inlineImages.push({ cid: cid, blob: new Blob([byteArr], { type: mimeType }), filename: cid + '.' + ext, mimeType: mimeType });
            return prefix + 'cid:' + cid + suffix;
        });
        return { body: rewritten, contentType: 'text/html; charset=utf-8', inlineImages: inlineImages };
    }
    return { body: document.getElementById('compose-body').value, contentType: 'text/plain; charset=utf-8', inlineImages: [] };
}

function setComposeBody(text, isHtml) {
    if (isHtml) {
        document.getElementById('compose-body-html').innerHTML = DOMPurify.sanitize(text);
        document.getElementById('compose-body').value = text;
    } else {
        document.getElementById('compose-body').value = text;
        document.getElementById('compose-body-html').innerHTML = escapeHtml(text).replace(/\n/g, '<br>');
    }
}

function openCompose(evt) {
    clearComposeForm();
    insertSignature();
    openTab(evt, 'compose-content', 'mail-tabcontent', 'mail-tablinks');
}

function replyToMail(mail, all, source) {
    replyContext = (source && source.account && source.mailbox) ? source : null;
    const subject = "Re: " + mail.subject;
    const from = (mail.from && mail.from.length > 0) ? mail.from[0] : null;
    const to = from ? formatMailAddress(from) : '';

    let cc = [];
    if (all) {
        const fromEmail = from ? `${from.MailboxName}@${from.HostName}`.toLowerCase() : '';
        const ownEmails = new Set();
        (accountsData || []).forEach(acc => {
            (acc.from_addresses || []).forEach(fa => {
                const e = (fa.email || fa.Email || '').toLowerCase();
                if (e) ownEmails.add(e);
            });
        });

        const seen = new Set([fromEmail]);
        const formatAddr = addr => {
            const email = `${addr.MailboxName}@${addr.HostName}`;
            const key = email.toLowerCase();
            if (seen.has(key) || ownEmails.has(key)) return null;
            seen.add(key);
            return formatMailAddress(addr);
        };

        cc = [...(mail.to || []), ...(mail.cc || [])]
            .map(formatAddr)
            .filter(Boolean);
    }

    const fromName = (from && from.PersonalName) || 'unknown sender';
    const attribution = `On ${new Date(mail.date).toLocaleString()}, ${fromName} wrote:`;
    const ct = mail.contentType || '';
    const isPlainOriginal = ct.startsWith('text/plain') || ct.startsWith('text/calendar');

    document.getElementById('compose-subject').value = subject;
    document.getElementById('compose-to').value = to;
    document.getElementById('compose-cc').value = cc.join(', ');
    if (isPlainOriginal) {
        const quoted = (mail.body || '').replace(/\r\n/g, '\n').replace(/\n/g, '\n> ');
        setComposeBody(`\n\n${attribution}\n> ${quoted}`, false);
    } else {
        // Same sanitizer config as the mail viewer, so the quote matches what was displayed
        const safeBody = DOMPurify.sanitize(mail.body || '', {
            ALLOW_DATA_ATTR: false,
            ADD_ATTR: ['style', 'bgcolor', 'color', 'background', 'align', 'valign', 'width', 'height', 'cellpadding', 'cellspacing', 'border'],
        });
        const quoteHtml = `<br><br><div>${escapeHtml(attribution)}</div>`
            + `<blockquote style="margin:0 0 0 0.8ex;border-left:2px solid #ccc;padding-left:1ex;">${safeBody}</blockquote>`;
        setComposeBody(quoteHtml, true);
    }
    insertSignature();

    openTab(null, 'compose-content', 'mail-tabcontent', 'mail-tablinks');
}

async function openDraft(accountName, mailboxName, uid) {
    const mail = await fetchMail(accountName, mailboxName, uid);
    document.getElementById('compose-to').value = formatMailAddressList(mail.to);
    document.getElementById('compose-cc').value = formatMailAddressList(mail.cc);
    document.getElementById('compose-bcc').value = formatMailAddressList(mail.bcc);
    document.getElementById('compose-subject').value = mail.subject;
    const isHtml = mail.contentType && mail.contentType.includes('html');
    setComposeBody(mail.body, isHtml);
    activeDraft = { account: accountName, mailbox: mailboxName, uid: uid };
    replyContext = null; // reply context is not persisted in drafts
    openTab(null, 'compose-content', 'mail-tabcontent', 'mail-tablinks');
}
