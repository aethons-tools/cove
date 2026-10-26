import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";

export default tseslint.config(
  { ignores: ["dist/**", "src/api/types.ts"] },
  ...tseslint.configs.recommended,
  {
    plugins: { "react-hooks": reactHooks },
    rules: {
      ...reactHooks.configs.recommended.rules,
      // Message HTML comes only from squawkrender (spec §4): the one allowed
      // site carries an eslint-disable comment saying so.
      "no-restricted-syntax": ["error", {
        selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']",
        message: "Only the squawkrender message body may set inner HTML (docs/web-app.md).",
      }],
    },
  },
);
