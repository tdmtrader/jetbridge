package main

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// tokenAuthUnaryInterceptor returns a grpc.UnaryServerInterceptor that checks
// the "authorization" metadata for a matching token.
func tokenAuthUnaryInterceptor(token string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := validateToken(ctx, token); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// tokenAuthStreamInterceptor returns a grpc.StreamServerInterceptor that checks
// the "authorization" metadata for a matching token.
func tokenAuthStreamInterceptor(token string) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := validateToken(ss.Context(), token); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}

// validateToken extracts the token from gRPC metadata and compares it against
// the expected value using constant-time comparison.
func validateToken(ctx context.Context, expected string) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing authorization token")
	}

	values := md.Get("authorization")
	if len(values) == 0 {
		return status.Error(codes.Unauthenticated, "missing authorization token")
	}

	token := values[0]
	token = strings.TrimPrefix(token, "Bearer ")

	if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
		return status.Error(codes.Unauthenticated, "invalid authorization token")
	}

	return nil
}

// loadServerTLS loads the server certificate/key and CA for client verification.
// Returns a grpc.ServerOption with TLS credentials configured for mTLS.
func loadServerTLS(certFile, keyFile, caFile string) (grpc.ServerOption, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load server cert/key: %w", err)
	}

	caBytes, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caBytes) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}

	return grpc.Creds(credentials.NewTLS(tlsConfig)), nil
}
