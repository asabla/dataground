import { type ReactNode, useState } from "react";
import { UNSAFE_PortalProvider } from "react-aria";
import { Dialog as AriaDialog, Heading, Modal, ModalOverlay } from "react-aria-components";

export interface DialogProps {
  title: string;
  children: ReactNode;
  isOpen: boolean;
  isDismissDisabled?: boolean;
  onClose: () => void;
}

/** The caller supplies an explicit close action and owns any pending command. */
export function Dialog({
  title,
  children,
  isOpen,
  isDismissDisabled = false,
  onClose,
}: DialogProps) {
  const [container, setContainer] = useState<HTMLDivElement | null>(null);
  return (
    <div className="dg-dialog-host" ref={setContainer}>
      {container && (
        // Keep the portal inside the caller's theme and visibility boundary.
        <UNSAFE_PortalProvider getContainer={() => container}>
          <ModalOverlay
            className="dg-dialog-overlay"
            isOpen={isOpen}
            isDismissable={false}
            isKeyboardDismissDisabled={isDismissDisabled}
            onOpenChange={(open) => {
              if (!open && !isDismissDisabled) onClose();
            }}
          >
            <Modal className="dg-dialog-modal">
              <AriaDialog className="dg-dialog">
                <Heading slot="title" className="dg-dialog__title">
                  {title}
                </Heading>
                {children}
              </AriaDialog>
            </Modal>
          </ModalOverlay>
        </UNSAFE_PortalProvider>
      )}
    </div>
  );
}
