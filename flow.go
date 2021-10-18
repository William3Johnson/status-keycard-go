package statuskeycardgo

import (
	"errors"

	"github.com/status-im/status-keycard-go/signal"
)

type KeycardFlow struct {
	flowType FlowType
	state    runState
	wakeUp   chan (struct{})
	storage  string
	params   map[string]interface{}
}

func NewFlow(storageDir string) (*KeycardFlow, error) {
	flow := &KeycardFlow{
		wakeUp:  make(chan (struct{})),
		storage: storageDir,
	}

	return flow, nil
}

func (f *KeycardFlow) Start(flowType FlowType, params FlowParams) error {
	if f.state != Idle {
		return errors.New("already running")
	}

	f.flowType = flowType
	f.params = params
	f.state = Running
	go f.runFlow()

	return nil
}

func (f *KeycardFlow) Resume(params FlowParams) error {
	if f.state != Paused {
		return errors.New("only paused flows can be resumed")
	}

	for k, v := range params {
		f.params[k] = v
	}

	f.state = Resuming
	f.wakeUp <- struct{}{}

	return nil
}

func (f *KeycardFlow) Cancel() error {
	prevState := f.state

	if prevState != Idle {
		return errors.New("cannot cancel idle flow")
	}

	f.state = Cancelling
	if prevState == Paused {
		f.wakeUp <- struct{}{}
	}

	return nil
}

func (f *KeycardFlow) runFlow() {
	repeat := true
	var result FlowStatus

	for repeat {
		switch f.flowType {
		case GetAppInfo:
			repeat, result = f.switchFlow()
		}
	}

	if f.state != Cancelling {
		signal.SendEvent(FlowResult, result)
	}

	f.state = Idle
}

func (f *KeycardFlow) switchFlow() (bool, FlowStatus) {
	kc := f.connect()
	defer f.closeKeycard(kc)

	if kc == nil {
		return false, FlowStatus{ErrorKey: ErrorConnection}
	}

	switch f.flowType {
	case GetAppInfo:
		return f.getAppInfoFlow(kc)
	default:
		return false, FlowStatus{ErrorKey: ErrorUnknownFlow}
	}
}

func (f *KeycardFlow) pause(action string, status FlowStatus) {
	signal.SendEvent(action, status)
	f.state = Paused
}

func (f *KeycardFlow) pauseAndWait(action string, status FlowStatus) bool {
	f.pause(action, status)
	<-f.wakeUp

	if f.state == Resuming {
		f.state = Running
		return true
	} else {
		return false
	}
}

func (f *KeycardFlow) closeKeycard(kc *keycardContext) {
	if kc != nil {
		kc.stop()
	}
}

func (f *KeycardFlow) connect() *keycardContext {
	kc, err := startKeycardContext()

	if err != nil {
		return nil
	}

	f.pause(InsertCard, FlowStatus{})
	select {
	case <-f.wakeUp:
		if f.state != Cancelling {
			panic("Resuming is not expected during connection")
		}
		return nil
	case <-kc.connected:
		if kc.runErr != nil {
			return nil
		}

		signal.SendEvent(CardInserted, FlowStatus{})
		return kc
	}
}

func (f *KeycardFlow) selectKeycard(kc *keycardContext) (bool, error) {
	appInfo, err := kc.selectApplet()

	if err != nil {
		return false, err
	}

	if !appInfo.Installed {
		if f.pauseAndWait(SwapCard, FlowStatus{ErrorKey: ErrorNotAKeycard}) {
			return true, nil
		} else {
			return false, errors.New(ErrorCancel)
		}
	}

	if requiredInstanceUID, ok := f.params[InstanceUID]; ok {
		if instanceUID := tox(appInfo.InstanceUID); instanceUID != requiredInstanceUID {
			if f.pauseAndWait(SwapCard, FlowStatus{ErrorKey: InstanceUID, InstanceUID: instanceUID}) {
				return true, nil
			} else {
				return false, errors.New(ErrorCancel)
			}
		}
	}

	if requiredKeyUID, ok := f.params[KeyUID]; ok {
		if keyUID := tox(appInfo.KeyUID); keyUID != requiredKeyUID {
			if f.pauseAndWait(SwapCard, FlowStatus{ErrorKey: KeyUID, KeyUID: keyUID}) {
				return true, nil
			} else {
				return false, errors.New(ErrorCancel)
			}
		}
	}

	return false, nil
}

func (f *KeycardFlow) getAppInfoFlow(kc *keycardContext) (bool, FlowStatus) {
	restart, err := f.selectKeycard(kc)

	if err != nil {
		return false, FlowStatus{ErrorKey: err.Error()}
	} else if restart {
		return true, nil
	}

	return false, FlowStatus{ErrorKey: ErrorOK, AppInfo: toAppInfo(kc.cmdSet.ApplicationInfo)}
}
