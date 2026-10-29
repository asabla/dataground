import { useState } from "react";
import { UNSAFE_PortalProvider } from "react-aria";
import { Menu, MenuItem, MenuTrigger, Popover } from "react-aria-components";
import { Button } from "./Button";

export interface ActionMenuItem {
  id: string;
  label: string;
  isDisabled?: boolean;
}
export interface ActionMenuProps {
  label: string;
  items: readonly ActionMenuItem[];
  isDisabled?: boolean;
  onAction: (id: string) => void;
}

export function ActionMenu({ label, items, isDisabled = false, onAction }: ActionMenuProps) {
  const [container, setContainer] = useState<HTMLDivElement | null>(null);
  return (
    <div className="dg-action-menu-host" ref={setContainer}>
      <UNSAFE_PortalProvider getContainer={() => container}>
        <MenuTrigger>
          <Button aria-label={label} isDisabled={isDisabled || items.length === 0} variant="quiet">
            More
          </Button>
          <Popover className="dg-action-menu-popover" placement="bottom end" isNonModal>
            <Menu
              aria-label={label}
              className="dg-action-menu"
              items={items}
              onAction={(id) => {
                const item = items.find((entry) => entry.id === id);
                if (!isDisabled && item && !item.isDisabled) onAction(item.id);
              }}
            >
              {(item) => (
                <MenuItem
                  id={item.id}
                  isDisabled={isDisabled || item.isDisabled}
                  className="dg-action-menu__item"
                >
                  {item.label}
                </MenuItem>
              )}
            </Menu>
          </Popover>
        </MenuTrigger>
      </UNSAFE_PortalProvider>
    </div>
  );
}
