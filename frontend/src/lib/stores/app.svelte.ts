import * as api from "../api";

export type Route = "loading" | "setup" | "main";

class App {
  route = $state<Route>("loading");
  loadError = $state(false);

  async load(): Promise<void> {
    this.loadError = false;
    try {
      const s = await api.getStatus();
      this.route = s.configured && s.certPinned ? "main" : "setup";
    } catch {
      this.loadError = true;
      this.route = "setup";
    }
  }

  setupDone(): void {
    this.route = "main";
    void api.syncStart().catch(() => {});
  }
}

export const app = new App();
