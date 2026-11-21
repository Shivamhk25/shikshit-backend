// Generates Zod schemas from the bundled OpenAPI contract (types come from openapi-typescript).
module.exports = {
  shikshit: {
    input: '../../backend/api/openapi/openapi.bundled.yaml',
    output: {
      mode: 'single',
      target: 'src/generated/zod.ts',
      client: 'zod',
    },
  },
};
