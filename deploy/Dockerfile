# 用 Docker 直接构建 Web 版镜像（可选）。
# 推荐用 ./build.sh web：不需要 Docker、不需要拉取基础镜像，直接生成可 docker load 的镜像 tar。
#
#   docker build -f deploy/Dockerfile -t datakit-manager:1.4.0 .
#   国内网络可加：--build-arg GOPROXY=https://goproxy.cn,direct --build-arg NPM_REGISTRY=https://registry.npmmirror.com
ARG GO_IMAGE=golang:1.26-alpine
ARG NODE_IMAGE=node:22-alpine

FROM ${NODE_IMAGE} AS ui
ARG NPM_REGISTRY=https://registry.npmjs.org
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund --registry=${NPM_REGISTRY}
COPY frontend/ ./
COPY internal/catalog/samples.json /src/internal/catalog/samples.json
RUN npm run build

FROM ${GO_IMAGE} AS build
ARG GOPROXY=https://proxy.golang.org,direct
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/frontend/dist ./frontend/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/datakit-manager-server ./cmd/server \
 && mkdir -p /out/data /out/tmp && chmod 1777 /out/tmp \
 && printf 'root:x:0:0:root:/:/sbin/nologin\nnonroot:x:65532:65532:nonroot:/data:/sbin/nologin\n' > /out/passwd

FROM scratch
COPY --from=build /out/passwd /etc/passwd
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build /out/tmp /tmp
COPY --from=build /out/datakit-manager-server /datakit-manager-server
ENV DKM_LISTEN=:8080 DKM_DATA_DIR=/data
USER 65532:65532
EXPOSE 8080
VOLUME /data
ENTRYPOINT ["/datakit-manager-server"]
