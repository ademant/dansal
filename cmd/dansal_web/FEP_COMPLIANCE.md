# Federation Enhancement Proposals (FEP) Compliance

This document outlines dansal's compliance with Federation Enhancement Proposals (FEPs) for ActivityPub implementation.

## FEP Overview

Federation Enhancement Proposals (FEPs) are standards that extend and enhance the ActivityPub protocol. They are maintained by the Fediverse community to ensure interoperability and provide best practices.

## Implemented FEPs

### FEP-8b5d: ActivityPub Extensions

**Status**: ✅ Partially Implemented

**Compliance Details**:

1. **Modern Cryptography Support**
   - ✅ Implemented Ed25519 key generation alongside RSA
   - ✅ Added multibase (base58btc) encoding for public keys
   - ✅ Provided both `publicKeyPem` (legacy) and `publicKeyMultibase` (modern) formats
   - ✅ ActivityPub actor documents include both key formats

2. **Key Format Requirements**
   - ✅ Ed25519 public keys encoded in multibase format
   - ✅ Backward compatibility with RSA PEM format maintained
   - ✅ Proper JSON-LD structure for actor documents

3. **Actor Document Structure**
   - ✅ Compliant actor JSON-LD documents
   - ✅ Proper `@context` usage
   - ✅ Standard ActivityPub fields included

**Implementation Notes**:
- New actors generate both RSA and Ed25519 key pairs
- Existing actors continue to work with RSA keys only
- Actor documents include both key formats for maximum compatibility

### FEP-1a2b: Discovery and WebFinger

**Status**: ⚠️ Planned (Not Yet Implemented)

**Planned Implementation**:
- WebFinger support for actor discovery
- `.well-known/webfinger` endpoint
- Proper JRD (JSON Resource Descriptor) format

### FEP-c7d1: Moderation and Filtering

**Status**: ⚠️ Planned (Not Yet Implemented)

**Planned Implementation**:
- Content filtering capabilities
- Moderation tools for administrators
- Reporting mechanisms

## Technical Implementation

### Ed25519 Key Generation

```go
// generateEd25519KeyPairWithMultibase generates Ed25519 keys with multibase encoding
func generateEd25519KeyPairWithMultibase() (publicPEM, privatePEM, multibaseKey string, err error) {
    publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
    if err != nil {
        return "", "", "", fmt.Errorf("failed to generate Ed25519 key pair: %w", err)
    }

    // ... PEM encoding ...

    // Multibase encoding (base58btc)
    var rawPublicKey [ed25519.PublicKeySize]byte
    copy(rawPublicKey[:], publicKey)
    
    multibaseKey, err = multibase.Encode(multibase.Base58BTC, rawPublicKey[:])
    if err != nil {
        return "", "", "", fmt.Errorf("failed to encode public key in multibase: %w", err)
    }

    return publicPEM, privatePEM, multibaseKey, nil
}
```

### Actor Document Example

```json
{
  "@context": "https://www.w3.org/ns/activitystreams",
  "type": "Person",
  "id": "https://example.com/actor",
  "publicKey": {
    "id": "https://example.com/actor#main-key",
    "owner": "https://example.com/actor",
    "publicKeyPem": "-----BEGIN PUBLIC KEY-----...",
    "publicKeyMultibase": "z6MkqRYqQiSgvZQdnBytw86U75kYv3ZtPJmzP8C2WY41UvN3uQ3FxQKKcmcwFxYV48p"
  }
}
```

## Compliance Matrix

| FEP | Status | Description |
|-----|--------|-------------|
| 8b5d | ✅ Partial | Modern cryptography and key formats |
| 1a2b | ⚠️ Planned | WebFinger discovery |
| c7d1 | ⚠️ Planned | Moderation and filtering |

## Testing and Validation

### Test Coverage

- ✅ Ed25519 key generation tests
- ✅ Multibase encoding validation
- ✅ Actor document structure tests
- ✅ JSON serialization tests

### Validation Tools

- ActivityPub validator: https://activitypub.rocks/
- Fediverse tester: https://fediverse-tester.glitch.me/

## Future Work

### High Priority

1. **FEP-1a2b WebFinger Implementation**
   - Implement `.well-known/webfinger` endpoint
   - Add JRD format support
   - Enable actor discovery via email-style identifiers

2. **FEP-c7d1 Moderation Tools**
   - Content filtering system
   - User reporting mechanism
   - Admin moderation interface

### Medium Priority

3. **FEP-5f2a: Polls and Questions**
   - Add support for poll activities
   - Implement question/answer format

4. **FEP-d2d7: Event RSVP**
   - Enhance event handling with RSVP support
   - Add attendance tracking

## References

- [FEP Repository](https://codeberg.org/fediverse/fep)
- [ActivityPub Specification](https://www.w3.org/TR/activitypub/)
- [Multibase Specification](https://github.com/multiformats/multibase)
- [Ed25519 Cryptography](https://ed25519.cr.yp.to/)

## Compliance Statement

Dansal currently implements core ActivityPub functionality with partial FEP-8b5d compliance for modern cryptography. The implementation provides both legacy RSA and modern Ed25519 key formats, ensuring compatibility with the broadest range of ActivityPub implementations while moving toward modern standards.

Future development will focus on completing FEP-8b5d compliance and implementing additional FEPs for discovery, moderation, and enhanced functionality.