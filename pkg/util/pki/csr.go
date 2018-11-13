/*
Copyright 2018 The Jetstack cert-manager contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pki

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	"github.com/jetstack/cert-manager/pkg/apis/certmanager/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CommonNameForCertificate returns the common name that should be used for the
// given Certificate resource, by inspecting the CommonName and DNSNames fields.
func CommonNameForCertificate(crt *v1alpha1.Certificate) string {
	if crt.Spec.CommonName != "" {
		return crt.Spec.CommonName
	}
	if len(crt.Spec.DNSNames) == 0 {
		return ""
	}
	return crt.Spec.DNSNames[0]
}

// DNSNamesForCertificate returns the DNS names that should be used for the
// given Certificate resource, by inspecting the CommonName and DNSNames fields.
func DNSNamesForCertificate(crt *v1alpha1.Certificate) []string {
	if len(crt.Spec.DNSNames) == 0 {
		if crt.Spec.CommonName == "" {
			return []string{}
		}
		return []string{crt.Spec.CommonName}
	}
	if crt.Spec.CommonName != "" {
		return removeDuplicates(append([]string{crt.Spec.CommonName}, crt.Spec.DNSNames...))
	}
	return crt.Spec.DNSNames
}

func removeDuplicates(in []string) []string {
	var found []string
Outer:
	for _, i := range in {
		for _, i2 := range found {
			if i2 == i {
				continue Outer
			}
		}
		found = append(found, i)
	}
	return found
}

const defaultOrganization = "cert-manager"

// OrganizationForCertificate will return the Organization to set for the
// Certificate resource.
// If an Organization is not specifically set, a default will be used.
func OrganizationForCertificate(crt *v1alpha1.Certificate) []string {
	if len(crt.Spec.Organization) == 0 {
		return []string{defaultOrganization}
	}

	return crt.Spec.Organization
}

var serialNumberLimit = new(big.Int).Lsh(big.NewInt(1), 128)

// default certification duration is 1 year
const defaultNotAfter = time.Hour * 24 * 365

// GenerateCSR will generate a new *x509.CertificateRequest template to be used
// by issuers that utilise CSRs to obtain Certificates.
// The CSR will not be signed, and should be passed to either EncodeCSR or
// to the x509.CreateCertificateRequest function.
func GenerateCSR(issuer v1alpha1.GenericIssuer, crt *v1alpha1.Certificate) (*x509.CertificateRequest, error) {
	commonName := CommonNameForCertificate(crt)
	dnsNames := DNSNamesForCertificate(crt)
	organization := OrganizationForCertificate(crt)

	if len(commonName) == 0 && len(dnsNames) == 0 {
		return nil, fmt.Errorf("no domains specified on certificate")
	}

	pubKeyAlgo, sigAlgo, err := SignatureAlgorithm(crt)
	if err != nil {
		return nil, err
	}

	return &x509.CertificateRequest{
		Version:            3,
		SignatureAlgorithm: sigAlgo,
		PublicKeyAlgorithm: pubKeyAlgo,
		Subject: pkix.Name{
			Organization: organization,
			CommonName:   commonName,
		},
		DNSNames: dnsNames,
		// TODO: work out how best to handle extensions/key usages here
		ExtraExtensions: []pkix.Extension{},
	}, nil
}

// GenerateTemplate will create a x509.Certificate for the given Certificate resource.
// This should create a Certificate template that is equivalent to the CertificateRequest
// generated by GenerateCSR.
// The PublicKey field must be populated by the caller.
func GenerateTemplate(issuer v1alpha1.GenericIssuer, crt *v1alpha1.Certificate) (*x509.Certificate, error) {
	commonName := CommonNameForCertificate(crt)
	dnsNames := DNSNamesForCertificate(crt)
	organization := OrganizationForCertificate(crt)

	if len(commonName) == 0 && len(dnsNames) == 0 {
		return nil, fmt.Errorf("no domains specified on certificate")
	}

	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to generate serial number: %s", err.Error())
	}

	pubKeyAlgo, _, err := SignatureAlgorithm(crt)
	if err != nil {
		return nil, err
	}

	keyUsages := x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
	if crt.Spec.IsCA {
		keyUsages |= x509.KeyUsageCertSign
	}

	expireTime := time.Now().Add(defaultNotAfter)
	metaExpireTime := metav1.NewTime(expireTime)
	crt.Status.NotAfter = &metaExpireTime

	return &x509.Certificate{
		Version:               3,
		BasicConstraintsValid: true,
		SerialNumber:          serialNumber,
		PublicKeyAlgorithm:    pubKeyAlgo,
		IsCA:                  crt.Spec.IsCA,
		Subject: pkix.Name{
			Organization: organization,
			CommonName:   commonName,
		},
		NotBefore: time.Now(),
		NotAfter:  expireTime,
		// see http://golang.org/pkg/crypto/x509/#KeyUsage
		KeyUsage: keyUsages,
		DNSNames: dnsNames,
	}, nil
}

// SignCertificate returns a signed x509.Certificate object for the given
// *v1alpha1.Certificate crt.
// publicKey is the public key of the signee, and signerKey is the private
// key of the signer.
// It returns a PEM encoded copy of the Certificate as well as a *x509.Certificate
// which can be used for reading the encoded values.
func SignCertificate(template *x509.Certificate, issuerCert *x509.Certificate, publicKey interface{}, signerKey interface{}) ([]byte, *x509.Certificate, error) {
	derBytes, err := x509.CreateCertificate(rand.Reader, template, issuerCert, publicKey, signerKey)

	if err != nil {
		return nil, nil, fmt.Errorf("error creating x509 certificate: %s", err.Error())
	}

	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("error decoding DER certificate bytes: %s", err.Error())
	}

	pemBytes := bytes.NewBuffer([]byte{})
	err = pem.Encode(pemBytes, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	if err != nil {
		return nil, nil, fmt.Errorf("error encoding certificate PEM: %s", err.Error())
	}

	// don't bundle the CA for selfsigned certificates
	// TODO: better comparison method here? for now we can just compare pointers.
	if issuerCert != template {
		// bundle the CA
		err = pem.Encode(pemBytes, &pem.Block{Type: "CERTIFICATE", Bytes: issuerCert.Raw})
		if err != nil {
			return nil, nil, fmt.Errorf("error encoding issuer cetificate PEM: %s", err.Error())
		}
	}

	return pemBytes.Bytes(), cert, err
}

// EncodeCSR calls x509.CreateCertificateRequest to sign the given CSR template.
// It returns a DER encoded signed CSR.
func EncodeCSR(template *x509.CertificateRequest, key crypto.Signer) ([]byte, error) {
	derBytes, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		return nil, fmt.Errorf("error creating x509 certificate: %s", err.Error())
	}

	return derBytes, nil
}

// EncodeX509 will encode a *x509.Certificate into PEM format.
func EncodeX509(cert *x509.Certificate) ([]byte, error) {
	caPem := bytes.NewBuffer([]byte{})
	err := pem.Encode(caPem, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err != nil {
		return nil, err
	}

	return caPem.Bytes(), nil
}

// SignatureAlgorithm will determine the appropriate signature algorithm for
// the given certificate.
// Adapted from https://github.com/cloudflare/cfssl/blob/master/csr/csr.go#L102
func SignatureAlgorithm(crt *v1alpha1.Certificate) (x509.PublicKeyAlgorithm, x509.SignatureAlgorithm, error) {
	var sigAlgo x509.SignatureAlgorithm
	var pubKeyAlgo x509.PublicKeyAlgorithm
	switch crt.Spec.KeyAlgorithm {
	case v1alpha1.KeyAlgorithm(""):
		// If keyAlgorithm is not specified, we default to rsa with keysize 2048
		pubKeyAlgo = x509.RSA
		sigAlgo = x509.SHA256WithRSA
	case v1alpha1.RSAKeyAlgorithm:
		pubKeyAlgo = x509.RSA
		switch {
		case crt.Spec.KeySize >= 4096:
			sigAlgo = x509.SHA512WithRSA
		case crt.Spec.KeySize >= 3072:
			sigAlgo = x509.SHA384WithRSA
		case crt.Spec.KeySize >= 2048:
			sigAlgo = x509.SHA256WithRSA
		default:
			return x509.UnknownPublicKeyAlgorithm, x509.UnknownSignatureAlgorithm, fmt.Errorf("unsupported rsa keysize specified: %d. min keysize %d", crt.Spec.KeySize, MinRSAKeySize)
		}
	case v1alpha1.ECDSAKeyAlgorithm:
		pubKeyAlgo = x509.ECDSA
		switch crt.Spec.KeySize {
		case 521:
			sigAlgo = x509.ECDSAWithSHA512
		case 384:
			sigAlgo = x509.ECDSAWithSHA384
		case 256:
			sigAlgo = x509.ECDSAWithSHA256
		default:
			return x509.UnknownPublicKeyAlgorithm, x509.UnknownSignatureAlgorithm, fmt.Errorf("unsupported ecdsa keysize specified: %d", crt.Spec.KeySize)
		}
	default:
		return x509.UnknownPublicKeyAlgorithm, x509.UnknownSignatureAlgorithm, fmt.Errorf("unsupported algorithm specified: %s. should be either 'ecdsa' or 'rsa", crt.Spec.KeyAlgorithm)
	}
	return pubKeyAlgo, sigAlgo, nil
}
