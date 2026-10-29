// Thin WebAuthn helpers on top of navigator.credentials - no dependency. The API sends
// and expects the standard JSON shapes with base64url-encoded binary fields.

export function b64urlToBytes(value: string): Uint8Array<ArrayBuffer> {
  const pad = '='.repeat((4 - (value.length % 4)) % 4)
  const bin = atob((value + pad).replace(/-/g, '+').replace(/_/g, '/'))
  const out = new Uint8Array(new ArrayBuffer(bin.length))
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

export function bytesToB64url(buf: ArrayBuffer | Uint8Array): string {
  const bytes = buf instanceof Uint8Array ? buf : new Uint8Array(buf)
  let bin = ''
  for (const b of bytes) bin += String.fromCharCode(b)
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/** True when this browser/context can do passkeys at all (needs HTTPS or localhost). */
export function passkeysSupported(): boolean {
  return (
    typeof window !== 'undefined' &&
    window.isSecureContext &&
    typeof window.PublicKeyCredential !== 'undefined' &&
    !!navigator.credentials
  )
}

interface DescriptorJSON {
  id: string
  type: PublicKeyCredentialType
  transports?: AuthenticatorTransport[]
}

function decodeDescriptors(list?: DescriptorJSON[]): PublicKeyCredentialDescriptor[] | undefined {
  return list?.map((d) => ({ ...d, id: b64urlToBytes(d.id) }))
}

/** Raised when the user dismisses the browser prompt - callers should stay quiet. */
export class PasskeyCancelled extends Error {}

function wrap(err: unknown): never {
  if (err instanceof DOMException && (err.name === 'NotAllowedError' || err.name === 'AbortError')) {
    throw new PasskeyCancelled()
  }
  throw err
}

// Options shape returned by the API (go-webauthn): { publicKey: {...} } with base64url fields.
export interface CreationOptionsJSON {
  publicKey: Omit<PublicKeyCredentialCreationOptions, 'challenge' | 'user' | 'excludeCredentials'> & {
    challenge: string
    user: { id: string; name: string; displayName: string }
    excludeCredentials?: DescriptorJSON[]
  }
}

export interface RequestOptionsJSON {
  publicKey: Omit<PublicKeyCredentialRequestOptions, 'challenge' | 'allowCredentials'> & {
    challenge: string
    allowCredentials?: DescriptorJSON[]
  }
}

export async function createPasskey(options: CreationOptionsJSON): Promise<unknown> {
  const pk = options.publicKey
  let cred: Credential | null
  try {
    cred = await navigator.credentials.create({
      publicKey: {
        ...pk,
        challenge: b64urlToBytes(pk.challenge),
        user: { ...pk.user, id: b64urlToBytes(pk.user.id) },
        excludeCredentials: decodeDescriptors(pk.excludeCredentials),
      },
    })
  } catch (err) {
    wrap(err)
  }
  if (!cred) throw new PasskeyCancelled()
  const c = cred as PublicKeyCredential
  const r = c.response as AuthenticatorAttestationResponse
  return {
    id: c.id,
    rawId: bytesToB64url(c.rawId),
    type: c.type,
    authenticatorAttachment: c.authenticatorAttachment ?? undefined,
    clientExtensionResults: c.getClientExtensionResults(),
    response: {
      clientDataJSON: bytesToB64url(r.clientDataJSON),
      attestationObject: bytesToB64url(r.attestationObject),
      transports: r.getTransports?.() ?? [],
    },
  }
}

export async function getPasskey(options: RequestOptionsJSON): Promise<unknown> {
  const pk = options.publicKey
  let cred: Credential | null
  try {
    cred = await navigator.credentials.get({
      publicKey: {
        ...pk,
        challenge: b64urlToBytes(pk.challenge),
        allowCredentials: decodeDescriptors(pk.allowCredentials),
      },
    })
  } catch (err) {
    wrap(err)
  }
  if (!cred) throw new PasskeyCancelled()
  const c = cred as PublicKeyCredential
  const r = c.response as AuthenticatorAssertionResponse
  return {
    id: c.id,
    rawId: bytesToB64url(c.rawId),
    type: c.type,
    authenticatorAttachment: c.authenticatorAttachment ?? undefined,
    clientExtensionResults: c.getClientExtensionResults(),
    response: {
      clientDataJSON: bytesToB64url(r.clientDataJSON),
      authenticatorData: bytesToB64url(r.authenticatorData),
      signature: bytesToB64url(r.signature),
      userHandle: r.userHandle ? bytesToB64url(r.userHandle) : undefined,
    },
  }
}
