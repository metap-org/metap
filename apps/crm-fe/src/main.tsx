import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { TooltipProvider, ToastProvider } from "@metap/ui";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter } from "react-router-dom";
import "@metap/ui/style.css";
import "./index.css";
import { ApiError } from "@metap/platform-ui";
import App from "./App";
import { ReactRouterNavigationProvider } from "./reactRouterNavigationAdapter";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (failureCount, error) => {
        if (error instanceof ApiError && error.status < 500) {
          return false;
        }
        return failureCount < 3;
      },
    },
  },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <TooltipProvider>
      <ToastProvider>
        <QueryClientProvider client={queryClient}>
          <BrowserRouter>
            <ReactRouterNavigationProvider>
              <App />
            </ReactRouterNavigationProvider>
          </BrowserRouter>
        </QueryClientProvider>
      </ToastProvider>
    </TooltipProvider>
  </StrictMode>,
);
