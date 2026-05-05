import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";

export default defineConfig({
  site: "https://microagent-kit.pages.dev",
  integrations: [
    starlight({
      title: "microagent-kit",
      description:
        "Run AI agent workspaces in microVMs. Firecracker on Linux, Apple Virtualization.framework on macOS.",
      social: {
        github: "https://github.com/geoffbelknap/microagent-kit",
      },
      editLink: {
        baseUrl:
          "https://github.com/geoffbelknap/microagent-kit/edit/main/site/",
      },
      sidebar: [
        {
          label: "Start here",
          items: [
            { label: "Overview", link: "/" },
            { label: "Install", link: "/getting-started/install/" },
            { label: "Run your first VM", link: "/getting-started/first-vm/" },
            {
              label: "Named workspaces",
              link: "/getting-started/named-workspaces/",
            },
          ],
        },
        {
          label: "Concepts",
          items: [
            { label: "Architecture", link: "/concepts/architecture/" },
            { label: "Backends", link: "/concepts/backends/" },
            { label: "Boundaries", link: "/concepts/boundaries/" },
            {
              label: "State and identity",
              link: "/concepts/state-and-identity/",
            },
          ],
        },
        {
          label: "CLI reference",
          items: [
            { label: "Overview", link: "/cli/" },
            { label: "run", link: "/cli/run/" },
            { label: "create", link: "/cli/create/" },
            { label: "start", link: "/cli/start/" },
            { label: "stop", link: "/cli/stop/" },
            { label: "kill", link: "/cli/kill/" },
            { label: "delete", link: "/cli/delete/" },
            { label: "status", link: "/cli/status/" },
            { label: "ps", link: "/cli/ps/" },
            { label: "logs", link: "/cli/logs/" },
            { label: "connect", link: "/cli/connect/" },
            { label: "doctor", link: "/cli/doctor/" },
            { label: "rootfs", link: "/cli/rootfs/" },
            { label: "kernel", link: "/cli/kernel/" },
            { label: "version", link: "/cli/version/" },
          ],
        },
        {
          label: "Protocol",
          items: [
            { label: "Apple VF supervisor", link: "/protocol/applevf/" },
          ],
        },
        {
          label: "Operations",
          items: [
            { label: "Smoke tests", link: "/operations/smoke-tests/" },
            { label: "Troubleshooting", link: "/operations/troubleshooting/" },
          ],
        },
        { label: "Security", link: "/security/" },
      ],
    }),
  ],
});
