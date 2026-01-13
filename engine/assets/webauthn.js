// WebAuthn helper functions for Conway passkey support
const ConwayWebAuthn = {
    // Check if WebAuthn is available
    isAvailable: function() {
        return window.PublicKeyCredential !== undefined;
    },

    // Check if platform authenticator is available (Touch ID, Face ID, Windows Hello)
    isPlatformAvailable: async function() {
        if (!this.isAvailable()) return false;
        try {
            return await PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable();
        } catch (e) {
            return false;
        }
    },

    // Check for conditional mediation support (autofill passkeys)
    isConditionalMediationAvailable: async function() {
        if (!this.isAvailable()) return false;
        try {
            return await PublicKeyCredential.isConditionalMediationAvailable();
        } catch (e) {
            return false;
        }
    },

    // Convert base64url to ArrayBuffer
    base64urlToBuffer: function(base64url) {
        const base64 = base64url.replace(/-/g, '+').replace(/_/g, '/');
        const padding = '='.repeat((4 - base64.length % 4) % 4);
        const binary = atob(base64 + padding);
        const bytes = new Uint8Array(binary.length);
        for (let i = 0; i < binary.length; i++) {
            bytes[i] = binary.charCodeAt(i);
        }
        return bytes.buffer;
    },

    // Convert ArrayBuffer to base64url
    bufferToBase64url: function(buffer) {
        const bytes = new Uint8Array(buffer);
        let binary = '';
        for (let i = 0; i < bytes.length; i++) {
            binary += String.fromCharCode(bytes[i]);
        }
        return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '');
    },

    // Register a new passkey
    register: async function() {
        // Start registration ceremony
        const beginResp = await fetch('/passkey/register/begin', {
            method: 'POST',
            credentials: 'include'
        });

        if (!beginResp.ok) {
            const err = await beginResp.text();
            throw new Error('Failed to start registration: ' + err);
        }

        const options = await beginResp.json();

        // Convert base64url strings to ArrayBuffers
        options.publicKey.challenge = this.base64urlToBuffer(options.publicKey.challenge);
        options.publicKey.user.id = this.base64urlToBuffer(options.publicKey.user.id);
        if (options.publicKey.excludeCredentials) {
            options.publicKey.excludeCredentials = options.publicKey.excludeCredentials.map(cred => ({
                ...cred,
                id: this.base64urlToBuffer(cred.id)
            }));
        }

        // Create credential
        const credential = await navigator.credentials.create(options);

        // Prepare response for server
        const response = {
            id: credential.id,
            rawId: this.bufferToBase64url(credential.rawId),
            type: credential.type,
            response: {
                clientDataJSON: this.bufferToBase64url(credential.response.clientDataJSON),
                attestationObject: this.bufferToBase64url(credential.response.attestationObject)
            }
        };

        if (credential.response.getTransports) {
            response.response.transports = credential.response.getTransports();
        }

        // Complete registration
        const finishResp = await fetch('/passkey/register/finish', {
            method: 'POST',
            credentials: 'include',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(response)
        });

        if (!finishResp.ok) {
            const err = await finishResp.text();
            throw new Error('Registration failed: ' + err);
        }

        return await finishResp.json();
    },

    // Login with passkey
    login: async function(callbackUri) {
        // Start login ceremony
        const beginResp = await fetch('/login/passkey/begin', {
            method: 'POST',
            credentials: 'include'
        });

        if (!beginResp.ok) {
            throw new Error('Failed to start login');
        }

        const options = await beginResp.json();

        // Convert challenge
        options.publicKey.challenge = this.base64urlToBuffer(options.publicKey.challenge);
        if (options.publicKey.allowCredentials) {
            options.publicKey.allowCredentials = options.publicKey.allowCredentials.map(cred => ({
                ...cred,
                id: this.base64urlToBuffer(cred.id)
            }));
        }

        // Get assertion
        const assertion = await navigator.credentials.get(options);

        // Prepare response
        const response = {
            id: assertion.id,
            rawId: this.bufferToBase64url(assertion.rawId),
            type: assertion.type,
            response: {
                clientDataJSON: this.bufferToBase64url(assertion.response.clientDataJSON),
                authenticatorData: this.bufferToBase64url(assertion.response.authenticatorData),
                signature: this.bufferToBase64url(assertion.response.signature),
                userHandle: assertion.response.userHandle
                    ? this.bufferToBase64url(assertion.response.userHandle)
                    : null
            }
        };

        // Complete login
        let finishUrl = '/login/passkey/finish';
        if (callbackUri) {
            finishUrl += '?callback_uri=' + encodeURIComponent(callbackUri);
        }

        const finishResp = await fetch(finishUrl, {
            method: 'POST',
            credentials: 'include',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(response)
        });

        if (!finishResp.ok) {
            throw new Error('Login failed');
        }

        const result = await finishResp.json();
        if (result.redirect) {
            window.location.href = result.redirect;
        }
        return result;
    },

    // Dismiss the passkey prompt
    dismissPrompt: async function() {
        await fetch('/passkey/dismiss-prompt', {
            method: 'POST',
            credentials: 'include'
        });
    },

    // Delete a passkey
    deletePasskey: async function(id) {
        const resp = await fetch('/passkey/' + id, {
            method: 'DELETE',
            credentials: 'include'
        });
        if (!resp.ok) {
            throw new Error('Failed to delete passkey');
        }
        return await resp.json();
    },

    // List passkeys
    listPasskeys: async function() {
        const resp = await fetch('/passkey/list', {
            credentials: 'include'
        });
        if (!resp.ok) {
            throw new Error('Failed to list passkeys');
        }
        return await resp.json();
    }
};
