package command

import (
	"fmt"
	"os"
)

const distributionCertificateImage = "alpine:3.24.2"

func writeDockerDistribution(root *os.Root, target buildTarget) error {
	platform := target.os + "/" + target.arch
	if target.arm != "" {
		platform += "/v" + target.arm
	}
	// 证书阶段使用构建机平台，最终 scratch 镜像也兼容 ARMv5 静态二进制。
	dockerfile := fmt.Sprintf(`# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM %s AS certificates
RUN apk add --no-cache ca-certificates && mkdir -p /runtime && chown 65532:65532 /runtime

FROM scratch
WORKDIR /app
COPY --from=certificates /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=certificates --chown=65532:65532 /runtime /app/runtime
COPY --chown=65532:65532 . /app/
COPY --chmod=0555 thinkgo-nocgo /app/thinkgo-nocgo
USER 65532:65532
ENV APP_SERVER_HOST=0.0.0.0 APP_SERVER_PORT=8000
EXPOSE 8000
ENTRYPOINT ["/app/thinkgo-nocgo"]
`, distributionCertificateImage)
	compose := fmt.Sprintf(`services:
  app:
    platform: %s
    build:
      context: .
      dockerfile: Dockerfile
    ports:
      - "${APP_PORT:-8000}:8000"
    environment:
      APP_SERVER_HOST: "0.0.0.0"
      APP_SERVER_PORT: "8000"
    volumes:
      - ./config:/app/config:ro
      - runtime:/app/runtime
    restart: unless-stopped
volumes:
  runtime:
`, platform)
	for name, content := range map[string]string{
		"Dockerfile": dockerfile, "docker-compose.yml": compose,
		".dockerignore": ".env\n.env.*\nruntime\nDockerfile\ndocker-compose.yml\n.dockerignore\n",
	} {
		if err := root.WriteFile(name, []byte(content), 0644); err != nil {
			return err
		}
	}
	return nil
}
