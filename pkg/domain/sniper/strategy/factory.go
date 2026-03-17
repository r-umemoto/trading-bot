package strategy

type SimpleStrategyFactory struct {
}

func (f *SimpleStrategyFactory) NewStrategy() Strategy {
	return &SampleStrategy{}
}

func init() {
	Register("simple", &SimpleStrategyFactory{})
}
