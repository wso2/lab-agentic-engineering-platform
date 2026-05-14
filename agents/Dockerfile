FROM node:22-alpine

WORKDIR /app
COPY package.json package-lock.json* ./
RUN npm install
COPY . .

USER 1000

EXPOSE 3400
CMD ["npx", "tsx", "src/server/index.ts"]
