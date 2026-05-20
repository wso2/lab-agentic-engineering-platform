FROM node:22-slim
WORKDIR /app
COPY package.json .
RUN npm install
COPY . .
EXPOSE 3400
CMD ["npm", "run", "start"]
